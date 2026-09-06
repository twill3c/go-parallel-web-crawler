package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// targetSite は API テスト用の小さな合成サイト。/ から /p1../pN へ、各ページから / へ戻る。
// delay があれば各応答を遅らせる(stop / 切断の検査用)。
func targetSite(t *testing.T, n int, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			fmt.Fprint(w, "<title>root</title>")
			for i := 1; i <= n; i++ {
				fmt.Fprintf(w, `<a href="/p%d">p</a>`, i)
			}
			return
		}
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<title>%s</title><a href="/">home</a><a href="/missing">x</a>`, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// apiServer は Server を httptest に載せる(実 HTTP でストリーミングと切断を検査するため)。
func apiServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer()
	s.AllowPrivate = true // httptest は 127.0.0.1(実サーバでは決して true にしない)
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

// sseEvent は SSE の 1 ブロック(event: 名と data: の JSON)。
type sseEvent struct {
	Name string
	Data model.Event
}

// readSSE は body を最後まで読んで SSE ブロックに分ける。
func readSSE(t *testing.T, r io.Reader) []sseEvent {
	t.Helper()
	var out []sseEvent
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var name string
	var data bytes.Buffer
	flush := func() {
		if data.Len() == 0 {
			return
		}
		var ev model.Event
		if err := json.Unmarshal(data.Bytes(), &ev); err != nil {
			t.Fatalf("data is not JSON: %v: %s", err, data.String())
		}
		out = append(out, sseEvent{Name: name, Data: ev})
		name = ""
		data.Reset()
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	return out
}

func postCrawl(t *testing.T, api *httptest.Server, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(api.URL+"/api/crawl", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/crawl: %v", err)
	}
	return resp
}

// T-301 / F-21, F-25: POST /api/crawl → text/event-stream・先頭 crawl_started・末尾 crawl_completed。
// event: 名は data.type と一致する。
func TestCrawl_Stream(t *testing.T) {
	target := targetSite(t, 4, 0)
	_, api := apiServer(t)
	resp := postCrawl(t, api, fmt.Sprintf(`{"url":%q,"workers":2,"maxPages":10,"requestDelayMs":0}`, target.URL))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	events := readSSE(t, resp.Body)
	if len(events) < 3 {
		t.Fatalf("events = %d", len(events))
	}
	if events[0].Name != model.EvCrawlStarted || events[0].Data.CrawlID == "" {
		t.Errorf("first = %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Name != model.EvCrawlCompleted || last.Data.Statistics == nil {
		t.Errorf("last = %+v", last)
	}
	for _, e := range events {
		if e.Name != e.Data.Type {
			t.Errorf("event: %q と data.type %q が食い違う", e.Name, e.Data.Type)
		}
	}
	// 陽性対照: 到達可能 6 ページ(/ + p1..p4 + /missing)が取れている
	if last.Data.Statistics.Total != 6 || last.Data.Statistics.Errors != 1 {
		t.Errorf("statistics = %+v", *last.Data.Statistics)
	}
}

// T-302 / F-22: 範囲外・不正 URL・内部 URL は 400 と error。期待値の出所: SPEC §2.5 §2.4。
func TestCrawl_Rejects(t *testing.T) {
	s := NewServer() // AllowPrivate=false: 内部 URL の拒否も検査する
	mux := http.NewServeMux()
	s.Register(mux)
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	cases := []struct{ name, body, want string }{
		{"workers 0", `{"url":"https://example.com","workers":0,"maxPages":10,"requestDelayMs":0}`, "workers"},
		{"workers 21", `{"url":"https://example.com","workers":21,"maxPages":10,"requestDelayMs":0}`, "workers"},
		{"maxPages 0", `{"url":"https://example.com","workers":1,"maxPages":0,"requestDelayMs":0}`, "maxPages"},
		{"maxPages 101", `{"url":"https://example.com","workers":1,"maxPages":101,"requestDelayMs":0}`, "maxPages"},
		{"delay -1", `{"url":"https://example.com","workers":1,"maxPages":1,"requestDelayMs":-1}`, "requestDelayMs"},
		{"delay 5001", `{"url":"https://example.com","workers":1,"maxPages":1,"requestDelayMs":5001}`, "requestDelayMs"},
		{"bad url", `{"url":"not a url","workers":1,"maxPages":1,"requestDelayMs":0}`, "url"},
		{"ftp", `{"url":"ftp://example.com/","workers":1,"maxPages":1,"requestDelayMs":0}`, "scheme"},
		{"localhost", `{"url":"http://localhost:3000/","workers":1,"maxPages":1,"requestDelayMs":0}`, "localhost"},
		{"loopback", `{"url":"http://127.0.0.1/","workers":1,"maxPages":1,"requestDelayMs":0}`, "loopback"},
		{"private", `{"url":"http://192.168.1.1/","workers":1,"maxPages":1,"requestDelayMs":0}`, "private"},
		{"not json", `{"url":`, "json"},
		{"empty", ``, "json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := postCrawl(t, api, c.body)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 400 {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, b)
			}
			var body map[string]string
			if err := json.Unmarshal(b, &body); err != nil || body["error"] == "" {
				t.Fatalf("body = %s", b)
			}
			if !strings.Contains(body["error"], c.want) {
				t.Errorf("error = %q, want to contain %q", body["error"], c.want)
			}
		})
	}
}

// T-303 / G-08: イベント列から統計を再計算して crawl_completed.statistics と一致させる(独立再計算)。
func TestCrawl_StatisticsRecomputed(t *testing.T) {
	target := targetSite(t, 6, 0)
	_, api := apiServer(t)
	resp := postCrawl(t, api, fmt.Sprintf(`{"url":%q,"workers":3,"maxPages":50,"requestDelayMs":0}`, target.URL))
	defer resp.Body.Close()
	events := readSSE(t, resp.Body)
	var durs []int64
	var total, success int
	for _, e := range events {
		if e.Name == model.EvPageCompleted {
			total++
			if e.Data.Error == "" {
				success++
			}
			durs = append(durs, e.Data.DurationMs)
		}
	}
	if total == 0 {
		t.Fatal("page_completed が無い")
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	var sum int64
	for _, d := range durs {
		sum += d
	}
	// SPEC §5: P95 = ceil(0.95 × n) 番目(1 始まり)、Avg = 平均(切り捨て)
	p95 := durs[int(math.Ceil(0.95*float64(len(durs))))-1]
	avg := sum / int64(len(durs))
	got := *events[len(events)-1].Data.Statistics
	if got.Total != total || got.Success != success || got.Errors != total-success || got.AvgMs != avg || got.P95Ms != p95 {
		t.Errorf("statistics = %+v, recomputed total=%d success=%d avg=%d p95=%d", got, total, success, avg, p95)
	}
	// Requests/sec = total / (durationMs/1000) を 0.1 刻みで
	if got.DurationMs > 0 {
		want := math.Round(float64(total)/(float64(got.DurationMs)/1000)*10) / 10
		if got.RequestsPerSec != want {
			t.Errorf("rps = %v, want %v", got.RequestsPerSec, want)
		}
	}
	// GET /api/crawl/{id}(F-24)が同じ統計を返す
	id := events[0].Data.CrawlID
	r2, err := http.Get(api.URL + "/api/crawl/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var res model.Result
	if err := json.NewDecoder(r2.Body).Decode(&res); err != nil || r2.StatusCode != 200 {
		t.Fatalf("GET /api/crawl/%s: status=%d err=%v", id, r2.StatusCode, err)
	}
	if res.Status != "completed" || res.Statistics != got || len(res.Pages) != total {
		t.Errorf("result = %+v", res)
	}
	if r3, _ := http.Get(api.URL + "/api/crawl/nope"); r3.StatusCode != 404 {
		t.Errorf("unknown id: status = %d", r3.StatusCode)
	}
}

// firstEvent は最初の SSE ブロックだけ読む(ストリームを閉じずに残す)。
func firstEvent(t *testing.T, r *bufio.Reader) model.Event {
	t.Helper()
	var data string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "" && data != "" {
			var ev model.Event
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatal(err)
			}
			return ev
		}
	}
}

// T-304 / F-23: /stop で進行中クロールが cancelled で終わる。
func TestCrawl_Stop(t *testing.T) {
	target := targetSite(t, 30, 150*time.Millisecond)
	_, api := apiServer(t)
	resp := postCrawl(t, api, fmt.Sprintf(`{"url":%q,"workers":2,"maxPages":100,"requestDelayMs":0}`, target.URL))
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	first := firstEvent(t, br)
	if first.Type != model.EvCrawlStarted {
		t.Fatalf("first = %+v", first)
	}
	time.Sleep(200 * time.Millisecond)
	r2, err := http.Post(api.URL+"/api/crawl/"+first.CrawlID+"/stop", "application/json", nil)
	if err != nil || r2.StatusCode != 200 {
		t.Fatalf("stop: %v %d", err, r2.StatusCode)
	}
	rest := readSSE(t, br)
	last := rest[len(rest)-1]
	if last.Name != model.EvCrawlCompleted || last.Data.Reason != model.ReasonCancelled {
		t.Errorf("last = %+v", last)
	}
	if last.Data.Statistics.Total >= 31 {
		t.Errorf("止まっていない: total=%d", last.Data.Statistics.Total)
	}
	if r3, _ := http.Post(api.URL+"/api/crawl/nope/stop", "application/json", nil); r3.StatusCode != 404 {
		t.Errorf("unknown id: status = %d", r3.StatusCode)
	}
}

// T-305 / F-09: クライアントが接続を切ると、サーバ側の Run が返る(台帳が cancelled で完了する)。
func TestCrawl_ClientDisconnect(t *testing.T) {
	target := targetSite(t, 30, 150*time.Millisecond)
	s, api := apiServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+"/api/crawl", strings.NewReader(
		fmt.Sprintf(`{"url":%q,"workers":2,"maxPages":100,"requestDelayMs":0}`, target.URL)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first := firstEvent(t, bufio.NewReader(resp.Body))
	time.Sleep(200 * time.Millisecond)
	cancel() // 切断
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if res, ok := s.registry.result(first.CrawlID); ok && res.Status == "completed" {
			if res.Reason != model.ReasonCancelled {
				t.Errorf("reason = %q", res.Reason)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("切断後 3 秒経っても Run が終わらない")
}
