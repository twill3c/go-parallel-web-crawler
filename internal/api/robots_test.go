package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// robotsTargetSite は robots.txt を持つ合成サイト。取得回数を数える。
func robotsTargetSite(t *testing.T, robotsBody string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			*hits++
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, robotsBody)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<title>%s</title><a href="/open/a">a</a><a href="/private/b">b</a>`, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// T-721: 既定(ignoreRobots を送らない)で robots.txt に従う。**欄が無い要求も従う側になる**。
func TestCrawl_RespectsRobotsByDefault(t *testing.T) {
	var hits int
	target := robotsTargetSite(t, "User-agent: *\nDisallow: /private/\n", &hits)
	_, api := apiServer(t)
	resp := postCrawl(t, api, fmt.Sprintf(`{"url":%q,"workers":2,"maxPages":10,"requestDelayMs":0}`, target.URL))
	defer resp.Body.Close()
	events := readSSE(t, resp.Body)
	if hits != 1 {
		t.Errorf("robots.txt の取得回数 = %d, want 1", hits)
	}
	if events[0].Data.Robots != model.RobotsObeyed {
		t.Errorf("crawl_started.robots = %q", events[0].Data.Robots)
	}
	for _, e := range events {
		if e.Name == model.EvPageCompleted && strings.Contains(e.Data.URL, "/private/") {
			t.Errorf("Disallow のページを取得した: %s", e.Data.URL)
		}
	}
	// 陽性対照: 除外が実際に起きた(結果の robotsBlocked が 0 でない)
	id := events[0].Data.CrawlID
	r2, err := http.Get(api.URL + "/api/crawl/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	var res model.Result
	if err := json.NewDecoder(r2.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.RobotsBlocked == 0 {
		t.Fatal("robotsBlocked = 0 —— 除外が働いていない(検査が何も見ていない)")
	}
	if res.Robots != model.RobotsObeyed {
		t.Errorf("result.robots = %q", res.Robots)
	}
}

// T-722: ignoreRobots=true なら取りに行かず、拒否されたページも取る。
func TestCrawl_IgnoreRobots(t *testing.T) {
	var hits int
	target := robotsTargetSite(t, "User-agent: *\nDisallow: /\n", &hits)
	_, api := apiServer(t)
	resp := postCrawl(t, api, fmt.Sprintf(`{"url":%q,"workers":2,"maxPages":10,"requestDelayMs":0,"ignoreRobots":true}`, target.URL))
	defer resp.Body.Close()
	events := readSSE(t, resp.Body)
	if hits != 0 {
		t.Errorf("ignoreRobots なのに robots.txt を %d 回取った", hits)
	}
	if events[0].Data.Robots != model.RobotsIgnored {
		t.Errorf("robots = %q, want ignored", events[0].Data.Robots)
	}
	var pages int
	for _, e := range events {
		if e.Name == model.EvPageCompleted {
			pages++
		}
	}
	if pages == 0 {
		t.Error("Disallow: / に従ってしまっている")
	}
}

// T-723: Disallow: / のサイトを既定で叩くと、1 ページも取らずに reason=robots で終わる。
func TestCrawl_RobotsDisallowAll(t *testing.T) {
	var hits int
	target := robotsTargetSite(t, "User-agent: *\nDisallow: /\n", &hits)
	_, api := apiServer(t)
	resp := postCrawl(t, api, fmt.Sprintf(`{"url":%q,"workers":2,"maxPages":10,"requestDelayMs":0}`, target.URL))
	defer resp.Body.Close()
	events := readSSE(t, resp.Body)
	last := events[len(events)-1]
	if last.Name != model.EvCrawlCompleted || last.Data.Reason != model.ReasonRobots {
		t.Fatalf("last = %+v", last)
	}
	if last.Data.Statistics.Total != 0 {
		t.Errorf("total = %d, want 0", last.Data.Statistics.Total)
	}
	// 画面が待ち続けないこと: 完了までのイベント数は少ないが、必ず閉じる
	if len(events) < 2 {
		t.Errorf("events = %d", len(events))
	}
}

// T-724: robots.txt の Crawl-delay が Request Delay の下限になる(API 経由でも効く)。
func TestCrawl_CrawlDelayThroughAPI(t *testing.T) {
	var hits int
	target := robotsTargetSite(t, "User-agent: *\nCrawl-delay: 0.15\n", &hits)
	_, api := apiServer(t)
	start := time.Now()
	resp := postCrawl(t, api, fmt.Sprintf(`{"url":%q,"workers":1,"maxPages":3,"requestDelayMs":0}`, target.URL))
	defer resp.Body.Close()
	events := readSSE(t, resp.Body)
	el := time.Since(start)
	if events[0].Data.CrawlDelayMs != 150 {
		t.Errorf("crawlDelayMs = %d, want 150", events[0].Data.CrawlDelayMs)
	}
	if events[0].Data.RequestDelayMs != 150 {
		t.Errorf("requestDelayMs = %d, want 150(Crawl-delay が下限になる)", events[0].Data.RequestDelayMs)
	}
	if el < 450*time.Millisecond {
		t.Errorf("3 ページで %v —— Crawl-delay が効いていない", el)
	}
}

// T-725: robots.txt が 5xx のときは complete disallow(RFC 9309 の MUST)。
func TestCrawl_RobotsUnreachableThroughAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<title>x</title>")
	}))
	t.Cleanup(srv.Close)
	_, api := apiServer(t)
	resp := postCrawl(t, api, fmt.Sprintf(`{"url":%q,"workers":1,"maxPages":5,"requestDelayMs":0}`, srv.URL))
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	events := readSSE(t, strings.NewReader(string(body)))
	if events[0].Data.Robots != model.RobotsUnreachable {
		t.Errorf("robots = %q", events[0].Data.Robots)
	}
	if events[len(events)-1].Data.Reason != model.ReasonRobots {
		t.Errorf("reason = %q", events[len(events)-1].Data.Reason)
	}
}
