package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// robotsSite は robots.txt を返す合成サイト。status で応答を切り替える。
func robotsSite(t *testing.T, robotsStatus int, robotsBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			if robotsStatus != 200 {
				w.WriteHeader(robotsStatus)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, robotsBody)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<title>%s</title><a href="/open/a">a</a><a href="/blocked/b">b</a><a href="/open/c">c</a>`, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runWithRobots(t *testing.T, srv *httptest.Server, cfg Config) ([]model.Event, model.Result) {
	t.Helper()
	cfg.StartURL = srv.URL + "/"
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.Workers == 0 {
		cfg.Workers = 2
	}
	if cfg.MaxPages == 0 {
		cfg.MaxPages = 20
	}
	cfg.Client = NewClient(cfg.StartURL, ClientOptions{Timeout: cfg.Timeout, AllowPrivate: true})
	cfg.RespectRobots = true
	var events []model.Event
	res := Run(context.Background(), cfg, func(e model.Event) { events = append(events, e) })
	return events, res
}

// T-711 / G-14: 200 の robots.txt に従い、Disallow のパスを取得しない。
func TestRun_RobotsDisallow(t *testing.T) {
	srv := robotsSite(t, 200, "User-agent: *\nDisallow: /blocked/\n")
	events, res := runWithRobots(t, srv, Config{})
	for _, p := range res.Pages {
		if strings.Contains(p.URL, "/blocked/") {
			t.Errorf("Disallow のページを取得した: %s", p.URL)
		}
	}
	// 前提の検算: 除外が実際に働いた(合成サイトは /blocked/b へのリンクを必ず出す)
	if res.RobotsBlocked == 0 {
		t.Fatal("除外件数が 0 —— 検査が何も見ていない")
	}
	// robots.txt の状態がイベントに出る
	var started *model.Event
	for i := range events {
		if events[i].Type == model.EvCrawlStarted {
			started = &events[i]
		}
	}
	if started == nil || started.Robots != "obeyed" {
		t.Fatalf("crawl_started.robots = %v", started)
	}
	// 開いているページは取れている
	if len(res.Pages) < 2 {
		t.Errorf("pages = %d(開いているページまで落としている)", len(res.Pages))
	}
}

// T-712 / G-14: 404(unavailable)はすべて許可(RFC 9309: MAY access any resources)。
func TestRun_RobotsNotFound(t *testing.T) {
	srv := robotsSite(t, 404, "")
	events, res := runWithRobots(t, srv, Config{})
	if res.RobotsBlocked != 0 {
		t.Errorf("404 なのに除外が起きた: %d", res.RobotsBlocked)
	}
	var blocked bool
	for _, p := range res.Pages {
		if strings.Contains(p.URL, "/blocked/") {
			blocked = true
		}
	}
	if !blocked {
		t.Error("404 のとき /blocked/ も取得できるはず")
	}
	if events[0].Robots != "absent" {
		t.Errorf("robots = %q, want absent", events[0].Robots)
	}
}

// T-713 / G-14: 5xx(unreachable)は complete disallow(RFC 9309 の MUST)。
// 1 ページも取らずに終わる。これは「安全側に倒す」ではなく規範の要求。
func TestRun_RobotsUnreachable(t *testing.T) {
	srv := robotsSite(t, 503, "")
	events, res := runWithRobots(t, srv, Config{})
	if len(res.Pages) != 0 {
		t.Errorf("5xx なのに %d ページ取得した", len(res.Pages))
	}
	if res.Reason != model.ReasonRobots {
		t.Errorf("reason = %q, want %q", res.Reason, model.ReasonRobots)
	}
	if events[0].Robots != "unreachable" {
		t.Errorf("robots = %q, want unreachable", events[0].Robots)
	}
	// 完了イベントは必ず出る(画面が待ち続けない)
	if events[len(events)-1].Type != model.EvCrawlCompleted {
		t.Error("crawl_completed が無い")
	}
}

// T-714: Crawl-delay は Request Delay の下限になる(利用者の値のほうが大きければそのまま)。
func TestRun_CrawlDelayIsFloor(t *testing.T) {
	srv := robotsSite(t, 200, "User-agent: *\nCrawl-delay: 0.2\n")
	// 利用者は 0 を指定 → robots の 200ms が下限として効く。3 ページで 600ms 以上
	start := time.Now()
	_, res := runWithRobots(t, srv, Config{Workers: 1, MaxPages: 3, RequestDelay: 0})
	if el := time.Since(start); el < 600*time.Millisecond {
		t.Errorf("Crawl-delay が効いていない(%v で %d ページ)", el, len(res.Pages))
	}
	if res.EffectiveDelayMs != 200 {
		t.Errorf("EffectiveDelayMs = %d, want 200", res.EffectiveDelayMs)
	}
	// 陰性対照: 利用者の値が大きければそちらを使う
	srv2 := robotsSite(t, 200, "User-agent: *\nCrawl-delay: 0.05\n")
	_, res2 := runWithRobots(t, srv2, Config{Workers: 1, MaxPages: 2, RequestDelay: 300 * time.Millisecond})
	if res2.EffectiveDelayMs != 300 {
		t.Errorf("EffectiveDelayMs = %d, want 300", res2.EffectiveDelayMs)
	}
}

// T-715: RespectRobots=false なら取りに行かない(既定の挙動を変えないことの確認)。
func TestRun_RobotsOptOut(t *testing.T) {
	var robotsHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			robotsHits++
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<title>x</title>")
	}))
	t.Cleanup(srv.Close)
	cfg := Config{StartURL: srv.URL + "/", Workers: 1, MaxPages: 2, Timeout: 2 * time.Second, RespectRobots: false}
	cfg.Client = NewClient(cfg.StartURL, ClientOptions{Timeout: cfg.Timeout, AllowPrivate: true})
	res := Run(context.Background(), cfg, func(model.Event) {})
	if robotsHits != 0 {
		t.Errorf("robots.txt を %d 回取りに行った", robotsHits)
	}
	if len(res.Pages) == 0 {
		t.Error("500 の robots.txt に引きずられて止まった")
	}
}
