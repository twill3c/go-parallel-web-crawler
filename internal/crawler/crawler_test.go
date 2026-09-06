package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// run は合成サイトに対してクロールを走らせ、イベント列と結果を返す共通経路。
// httptest は 127.0.0.1 なので AllowPrivate で SSRF 検査を外す(実サーバでは外さない)。
func run(t *testing.T, ctx context.Context, s *site, cfg Config) ([]model.Event, model.Result) {
	t.Helper()
	srv := s.serve()
	t.Cleanup(srv.Close)
	cfg.StartURL = srv.URL + s.root
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	cfg.Client = NewClient(cfg.StartURL, ClientOptions{Timeout: cfg.Timeout, AllowPrivate: true})
	var events []model.Event
	res := Run(ctx, cfg, func(e model.Event) { events = append(events, e) })
	return events, res
}

func pathsOf(pages []model.Page, base string) map[string]bool {
	m := map[string]bool{}
	for _, p := range pages {
		m[strings.TrimPrefix(p.URL, base)] = true
	}
	return m
}

// T-201 / F-10, G-05: 到達可能 ≥ maxPages のサイトを maxPages=10 で → 取得 = 10、reason = max_pages。
func TestRun_MaxPages(t *testing.T) {
	s := treeSite(3, 3, 2) // 到達可能 1+3+9+27 = 40(生成関数から導出して検算する)
	if n := len(s.reachable()); n < 10 {
		t.Fatalf("前提: 到達可能 %d < 10", n)
	}
	_, res := run(t, context.Background(), s, Config{Workers: 4, MaxPages: 10})
	if len(res.Pages) != 10 {
		t.Fatalf("pages = %d, want 10", len(res.Pages))
	}
	if res.Reason != model.ReasonMaxPages {
		t.Fatalf("reason = %q, want max_pages", res.Reason)
	}
}

// T-202 / F-03, G-05: maxPages=100 で → 取得 = 到達可能数、到達不能(orphan)は取らない、reason = exhausted。
func TestRun_Exhausts(t *testing.T) {
	s := treeSite(2, 3, 3) // 到達可能 1+2+4+8 = 15、orphan 3
	want := s.reachable()
	if len(want) >= 100 || len(want) == 0 {
		t.Fatalf("前提: 到達可能 %d は 1..99 であるべき", len(want))
	}
	events, res := run(t, context.Background(), s, Config{Workers: 3, MaxPages: 100})
	got := pathsOf(res.Pages, strings.TrimSuffix(res.Pages[0].URL, "/"))
	for p := range want {
		if !got[p] {
			t.Errorf("到達可能ページ %s が未取得", p)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("到達不能ページ %s を取得した", p)
		}
	}
	if res.Reason != model.ReasonExhausted {
		t.Errorf("reason = %q, want exhausted", res.Reason)
	}
	for _, p := range res.Pages {
		if p.Error != "" || p.StatusCode != 200 || !strings.HasPrefix(p.Title, "Page ") {
			t.Errorf("page %+v: 成功ページの記録が不正", p)
		}
	}
	// 統計: total = success + errors、errors = 0
	st := res.Statistics
	if st.Total != len(res.Pages) || st.Errors != 0 || st.Success != st.Total {
		t.Errorf("statistics %+v が pages と食い違う", st)
	}
	// イベント列の先頭と末尾(F-25)
	if events[0].Type != model.EvCrawlStarted || events[len(events)-1].Type != model.EvCrawlCompleted {
		t.Errorf("events の先頭/末尾: %s / %s", events[0].Type, events[len(events)-1].Type)
	}
}

// T-203 / F-04, G-06: 応答を 150 ms 遅らせると、同時接続数の最大 = workers(N=1,3,5)。
// 前提の検算: 到達可能ページ数が workers より十分多い(並列度が飽和する)。
func TestRun_ConcurrencyEqualsWorkers(t *testing.T) {
	for _, n := range []int{1, 3, 5} {
		s := treeSite(5, 2, 0) // 到達可能 31
		if len(s.reachable()) < 4*n {
			t.Fatalf("前提: 到達可能 %d < 4×%d", len(s.reachable()), n)
		}
		s.delay = 150 * time.Millisecond
		_, res := run(t, context.Background(), s, Config{Workers: n, MaxPages: 100})
		if len(res.Pages) != len(s.reachable()) {
			t.Fatalf("workers=%d: pages=%d", n, len(res.Pages))
		}
		if int(s.maxCur) != n {
			t.Errorf("workers=%d: 同時接続の最大 = %d, want %d", n, s.maxCur, n)
		}
	}
}

// T-204 / F-09, G-07: 開始 200 ms 後に cancel → cancel 後に始まった取得 0・Run が返る・reason = cancelled。
func TestRun_Cancel(t *testing.T) {
	s := treeSite(4, 3, 0) // 到達可能 85
	s.delay = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	var cancelledNano atomic.Int64
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancelledNano.Store(time.Now().UnixNano())
		cancel()
	}()
	done := make(chan struct{})
	var res model.Result
	var events []model.Event
	go func() {
		events, res = run(t, ctx, s, Config{Workers: 3, MaxPages: 100})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel 後 3 秒経っても Run が返らない")
	}
	if res.Reason != model.ReasonCancelled {
		t.Errorf("reason = %q, want cancelled", res.Reason)
	}
	cancelledAt := time.Unix(0, cancelledNano.Load())
	// 前提の検算: cancel より前に始まった取得が実際にあった(cancel が「何もしていないとき」に起きていない)
	starts := s.requestStarts()
	if len(starts) == 0 || !starts[0].Before(cancelledAt) {
		t.Fatalf("前提: cancel 前に取得が始まっていない(starts=%d)", len(starts))
	}
	for _, st := range starts {
		if st.After(cancelledAt) {
			t.Errorf("cancel(%v)の後に取得が始まった: %v", cancelledAt, st)
		}
	}
	// 全 Worker の worker_done が届いている(goroutine が抜けた)
	done3 := 0
	for _, e := range events {
		if e.Type == model.EvWorkerDone {
			done3++
		}
	}
	if done3 != 3 {
		t.Errorf("worker_done = %d, want 3", done3)
	}
	// 取得が中断されたページは Cancelled として記録される(§22 Context Cancelled)。取り込みが完了したページは成功
	for _, p := range res.Pages {
		if p.Error != "" && p.Error != model.ErrCancelled {
			t.Errorf("page %s: error=%q", p.URL, p.Error)
		}
	}
}

// T-205 / F-08: image/png を返す URL は Non HTML・リンク抽出しない。
func TestRun_NonHTML(t *testing.T) {
	s := treeSite(2, 1, 0) // /, /n0, /n1
	s.nonHTML["/n0"] = "image/png"
	_, res := run(t, context.Background(), s, Config{Workers: 2, MaxPages: 100})
	var found bool
	for _, p := range res.Pages {
		if strings.HasSuffix(p.URL, "/n0") {
			found = true
			if p.Error != model.ErrNonHTML {
				t.Errorf("/n0 error = %q, want Non HTML", p.Error)
			}
		}
		if strings.HasSuffix(p.URL, "/should-not-follow") {
			t.Errorf("非 HTML の中身からリンクを辿った")
		}
	}
	if !found {
		t.Fatal("/n0 が取得されていない")
	}
	if res.Statistics.Errors != 1 {
		t.Errorf("errors = %d, want 1", res.Statistics.Errors)
	}
}

// T-206 / F-11: 404 / 500 / タイムアウト / リダイレクト過多 / 別ドメインへのリダイレクト の種別。
func TestRun_ErrorKinds(t *testing.T) {
	s := treeSite(1, 0, 0)
	s.pages["/"] = []string{"/nf", "/ise", "/slow", "/loop", "/away"}
	s.pages["/nf"] = nil
	s.status["/nf"] = 404
	s.pages["/ise"] = nil
	s.status["/ise"] = 500
	base := s.handler()
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.org/", http.StatusFound)
	})
	mux.Handle("/", base)
	srv2 := httptest.NewServer(mux)
	t.Cleanup(srv2.Close)
	cfg := Config{StartURL: srv2.URL + "/", Workers: 2, MaxPages: 10, Timeout: 300 * time.Millisecond}
	cfg.Client = NewClient(cfg.StartURL, ClientOptions{Timeout: cfg.Timeout, AllowPrivate: true})
	res := Run(context.Background(), cfg, func(model.Event) {})
	want := map[string]string{
		"/nf":   "HTTP 404",
		"/ise":  "HTTP 500",
		"/slow": model.ErrTimeout,
		"/loop": model.ErrTooManyRedirects,
		"/away": model.ErrOffDomain,
	}
	got := map[string]string{}
	for _, p := range res.Pages {
		got[strings.TrimPrefix(p.URL, srv2.URL)] = p.Error
	}
	for path, e := range want {
		if got[path] != e {
			t.Errorf("%s: error = %q, want %q", path, got[path], e)
		}
	}
	if res.Statistics.Errors != len(want) || res.Statistics.Success != 1 {
		t.Errorf("statistics = %+v", res.Statistics)
	}
	// エラーページからはリンクを辿らない(404/500 の本文に /from-error-page がある)
	if got["/from-error-page"] != "" || len(res.Pages) != len(want)+1 {
		t.Errorf("エラーページの本文からリンクを辿った: pages=%d", len(res.Pages))
	}
}

// T-207 / F-06, G-12: 辺は (from,to) で一意。外部ドメインは出ない。辺集合 = サイトの到達可能な辺集合。
func TestRun_LinksUnique(t *testing.T) {
	s := treeSite(2, 2, 0)
	events, res := run(t, context.Background(), s, Config{Workers: 2, MaxPages: 100})
	base := strings.TrimSuffix(res.Pages[0].URL, "/")
	seen := map[[2]string]bool{}
	queuedTo := map[string]int{}
	for _, e := range events {
		if e.Type != model.EvLinkFound {
			continue
		}
		if strings.Contains(e.To, "other.example.org") {
			t.Errorf("外部ドメインの辺: %s -> %s", e.From, e.To)
		}
		k := [2]string{strings.TrimPrefix(e.From, base), strings.TrimPrefix(e.To, base)}
		if seen[k] {
			t.Errorf("重複した辺: %v", k)
		}
		seen[k] = true
		if e.Queued {
			queuedTo[e.To]++
		}
	}
	// queued=true の辺は「未知の URL がキューに入った」印なので、各 to について一度だけ(BFS の木)
	for to, n := range queuedTo {
		if n != 1 {
			t.Errorf("%s が %d 回キューに入った", to, n)
		}
	}
	// イベントの辺集合 = res.Links = サイトの辺集合(閉路・重複を潰したもの)
	got := map[[2]string]bool{}
	for _, l := range res.Links {
		got[[2]string{strings.TrimPrefix(l.From, base), strings.TrimPrefix(l.To, base)}] = true
	}
	if len(got) != len(seen) {
		t.Errorf("res.Links %d 本と link_found %d 本が食い違う", len(got), len(seen))
	}
	want := s.edges()
	for k := range want {
		if !got[k] {
			t.Errorf("辺 %v が無い", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("余分な辺 %v", k)
		}
	}
}

// T-208 / F-13: 各 Worker について started/completed が交互で、最後に worker_done。
func TestRun_WorkerTransitions(t *testing.T) {
	s := treeSite(3, 2, 0)
	events, _ := run(t, context.Background(), s, Config{Workers: 3, MaxPages: 100})
	state := map[int]string{}
	done := map[int]bool{}
	for _, e := range events {
		switch e.Type {
		case model.EvWorkerStarted:
			if state[e.WorkerID] == "crawling" || done[e.WorkerID] {
				t.Fatalf("worker %d: started が連続 / done 後", e.WorkerID)
			}
			state[e.WorkerID] = "crawling"
		case model.EvPageCompleted:
			if state[e.WorkerID] != "crawling" {
				t.Fatalf("worker %d: completed が started 無しに来た", e.WorkerID)
			}
			state[e.WorkerID] = "waiting"
		case model.EvWorkerDone:
			if state[e.WorkerID] == "crawling" {
				t.Fatalf("worker %d: crawling のまま done", e.WorkerID)
			}
			done[e.WorkerID] = true
		}
	}
	if len(done) != 3 {
		t.Fatalf("worker_done を出した Worker = %d, want 3", len(done))
	}
}

// T-209 / F-14: requestDelayMs=100・workers=1・5 ページ → 総時間 ≥ 5 × 100 ms。
func TestRun_Delay(t *testing.T) {
	s := treeSite(4, 1, 0) // 5 ページ
	if len(s.reachable()) != 5 {
		t.Fatalf("前提: %d ページ", len(s.reachable()))
	}
	start := time.Now()
	_, res := run(t, context.Background(), s, Config{Workers: 1, MaxPages: 100, RequestDelay: 100 * time.Millisecond})
	if el := time.Since(start); el < 500*time.Millisecond {
		t.Errorf("elapsed %v < 500ms", el)
	}
	if res.Statistics.DurationMs < 500 {
		t.Errorf("statistics.durationMs = %d < 500", res.Statistics.DurationMs)
	}
}

// T-210 / F-15: 上限時間 300 ms・遅いサーバ → reason = deadline。
func TestRun_Deadline(t *testing.T) {
	s := treeSite(3, 3, 0)
	s.delay = 100 * time.Millisecond
	_, res := run(t, context.Background(), s, Config{Workers: 2, MaxPages: 100, Deadline: 300 * time.Millisecond})
	if res.Reason != model.ReasonDeadline {
		t.Errorf("reason = %q, want deadline", res.Reason)
	}
	if len(res.Pages) >= len(s.reachable()) {
		t.Errorf("上限時間内に全ページ取れてしまった(前提が崩れている)")
	}
}

// G-08 の前段: 統計は pages から決定的に計算できる(独立再計算)。
func TestComputeStatistics(t *testing.T) {
	pages := []model.Page{
		{DurationMs: 10}, {DurationMs: 20}, {DurationMs: 30}, {DurationMs: 40, Error: "HTTP 404"},
		{DurationMs: 50}, {DurationMs: 60}, {DurationMs: 70}, {DurationMs: 80}, {DurationMs: 90}, {DurationMs: 1000},
	}
	st := ComputeStatistics(pages, 2000*time.Millisecond)
	// P95: n=10 → ceil(9.5)=10 番目 = 1000(SPEC §5 の定義から導出)
	if st.P95Ms != 1000 || st.AvgMs != 145 || st.Total != 10 || st.Success != 9 || st.Errors != 1 {
		t.Errorf("statistics = %+v", st)
	}
	if st.RequestsPerSec != 5 {
		t.Errorf("rps = %v, want 5", st.RequestsPerSec)
	}
	// n=1 → ceil(0.95)=1 番目
	if one := ComputeStatistics(pages[:1], time.Second); one.P95Ms != 10 {
		t.Errorf("n=1: p95 = %d", one.P95Ms)
	}
	if zero := ComputeStatistics(nil, time.Second); zero.Total != 0 || zero.P95Ms != 0 {
		t.Errorf("n=0: %+v", zero)
	}
}
