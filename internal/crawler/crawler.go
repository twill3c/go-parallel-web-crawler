package crawler

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// Config はクロール 1 回分の設定。境界の検査は API 層(F-22)で済ませてから渡す。
type Config struct {
	CrawlID      string
	StartURL     string        // 正規化前でもよい。Run が Normalize する
	Workers      int           // 1..20
	MaxPages     int           // 1..100
	RequestDelay time.Duration // Worker ごとの取得前スリープ(D-05)
	Timeout      time.Duration // 1 リクエストの上限(既定 5 s)
	Deadline     time.Duration // クロール全体の上限(既定 60 s)
	Client       *http.Client  // nil なら NewClient(StartURL, Timeout) を使う
	// RespectRobots が真なら、開始前に robots.txt を 1 回取得して規則に従う(RFC 9309)。
	// 偽なら取得しない —— 自分が管理するサイトを試すときのための逃げ道で、画面は既定で真にする
	RespectRobots bool
	// UseSitemap が真なら、robots.txt の Sitemap 行(無ければ /sitemap.xml)から種 URL を足す。
	// 同一ドメイン制限と上限ページ数は変わらない(原本 §34 B)
	UseSitemap bool
}

// 既定値(SPEC §2.5)。
const (
	DefaultTimeout  = 5 * time.Second
	DefaultDeadline = 60 * time.Second
)

// Run はクロールを最後まで(または ctx の終了まで)走らせ、イベントを sink に順に渡し、結果を返す。
//
//	               jobs channel(容量 = maxPages)
//	                    │
//	    ┌───────────────┼───────────────┐
//	    ↓               ↓               ↓
//	Worker 1        Worker 2        Worker N       ← goroutine × N
//	    │               │               │
//	    └───────────────┼───────────────┘
//	                    ↓
//	              results channel  → manager(この関数)が集約し、新しい URL を jobs へ
//
// manager は「送った job の数 − 返ってきた result の数」(inflight)が 0 になった時点で
// jobs を閉じる。Worker は jobs が閉じられると range を抜けて worker_done を出し、
// WaitGroup が全員の終了を待つ。events channel は Worker と manager の両方が書き、
// 専用の goroutine が順に sink へ渡す(sink は 1 本の goroutine からしか呼ばれない)。
func Run(parent context.Context, cfg Config, sink func(model.Event)) model.Result {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Deadline <= 0 {
		cfg.Deadline = DefaultDeadline
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.MaxPages < 1 {
		cfg.MaxPages = 1
	}
	start, err := Normalize(cfg.StartURL)
	if err != nil {
		return model.Result{Status: "error", Reason: "invalid start url"}
	}
	if cfg.Client == nil {
		cfg.Client = NewClient(start, ClientOptions{Timeout: cfg.Timeout})
	}

	ctx, cancel := context.WithTimeout(parent, cfg.Deadline)
	defer cancel()

	t0 := time.Now()
	clock := func() int64 { return time.Since(t0).Milliseconds() }

	// robots.txt を 1 回だけ取る(同一ドメインしか辿らないのでホストは 1 つ)
	robots, robotsState := fetchRobots(ctx, cfg)
	if robots != nil && robots.CrawlDelay > cfg.RequestDelay {
		cfg.RequestDelay = robots.CrawlDelay // Crawl-delay は下限として効かせる
	}

	jobs := make(chan string, cfg.MaxPages) // D-06: URLSet が maxPages で止まるので送信は詰まらない
	results := make(chan result)
	events := make(chan model.Event, 64)

	// イベントの配送(sink を呼ぶのはこの goroutine だけ)
	var deliver sync.WaitGroup
	deliver.Add(1)
	go func() {
		defer deliver.Done()
		for e := range events {
			sink(e)
		}
	}()

	// サイトマップの取得(原本 §34 B)。robots.txt が Sitemap 行を持てばそれを、無ければ /sitemap.xml を試す
	var sitemapSeeds []string
	sitemapState := model.SitemapSkipped
	if cfg.UseSitemap && robotsState != model.RobotsUnreachable {
		sitemapSeeds, sitemapState = fetchSitemap(ctx, cfg, start, robots)
	}

	crawlDelayMs := 0
	if robots != nil {
		crawlDelayMs = int(robots.CrawlDelay / time.Millisecond)
	}
	events <- model.Event{
		Type: model.EvCrawlStarted, T: 0, CrawlID: cfg.CrawlID, URL: start,
		Workers: cfg.Workers, MaxPages: cfg.MaxPages, RequestDelayMs: int(cfg.RequestDelay / time.Millisecond),
		StartedAt: t0.UTC().Format(time.RFC3339Nano),
		Robots:    robotsState, CrawlDelayMs: crawlDelayMs,
		Sitemap: sitemapState, SitemapURLs: len(sitemapSeeds),
	}

	// robots.txt が到達不能なら 1 ページも取らずに終える(RFC 9309 の MUST)。
	// 開始 URL 自体が Disallow のときも同じ扱い —— 1 ページも取れないので始めない
	if robotsState == model.RobotsUnreachable || (robots != nil && !robots.AllowsURL(start)) {
		if robotsState != model.RobotsUnreachable {
			robotsState = model.RobotsObeyed
		}
		stats := ComputeStatistics(nil, time.Since(t0))
		events <- model.Event{Type: model.EvCrawlCompleted, T: clock(), Reason: model.ReasonRobots, Statistics: &stats}
		close(events)
		deliver.Wait()
		close(jobs)
		return model.Result{
			Status: "completed", Reason: model.ReasonRobots, Statistics: stats,
			Robots: robotsState, EffectiveDelayMs: int(cfg.RequestDelay / time.Millisecond),
		}
	}

	// Worker Pool
	fetcher := &Fetcher{Client: cfg.Client}
	var wg sync.WaitGroup
	for id := 1; id <= cfg.Workers; id++ {
		wg.Add(1)
		go worker(ctx, id, jobs, results, events, fetcher, cfg.RequestDelay, clock, &wg)
	}

	// manager: BFS
	seen := NewURLSet(cfg.MaxPages)
	var pages []model.Page
	var links []model.Link
	edgeSeen := map[model.Link]bool{}
	robotsBlockedSeen := map[string]bool{}
	robotsBlocked := 0
	external := NewExternalTally() // 外部ドメインは辿らずに数える(原本 §34 C)
	capped := false
	inflight := 0

	seen.Add(start)
	jobs <- start
	inflight++

	// サイトマップの URL を種として足す(原本 §34 B)。同一ドメイン・robots・上限は開始 URL と同じ扱い。
	// **辿る順は BFS のまま**で、ここで足したものは開始 URL の次に並ぶ
	sitemapAdded := 0
	for _, u := range sitemapSeeds {
		if seen.Len() >= cfg.MaxPages {
			capped = true
			break
		}
		if !SameDomain(start, u) || LooksNonHTML(u) || seen.Has(u) {
			continue
		}
		if robots != nil && !robots.AllowsURL(u) {
			if !robotsBlockedSeen[u] {
				robotsBlockedSeen[u] = true
				robotsBlocked++
			}
			continue
		}
		if seen.Add(u) {
			jobs <- u
			inflight++
			sitemapAdded++
		}
	}

	for inflight > 0 {
		r := <-results
		inflight--
		if r.skipped {
			continue
		}
		pages = append(pages, r.page)
		if ctx.Err() != nil {
			continue // 止められた後は新しい URL を足さない(G-07)
		}
		for _, l := range r.links {
			if !SameDomain(start, l) {
				// 別ドメインは辿らない。**数えるだけ**(原本 §34 C)。
				// 同じ (from,to) を二度数えないよう、辺として既知かで抑える
				e := model.Link{From: r.page.URL, To: l}
				if !edgeSeen[e] {
					edgeSeen[e] = true
					external.Add(r.page.URL, l)
					events <- model.Event{Type: model.EvExternalFound, T: clock(), From: r.page.URL, To: l}
				}
				continue
			}
			if LooksNonHTML(l) {
				continue
			}
			if robots != nil && !robots.AllowsURL(l) {
				// robots.txt が拒否した URL は辿らない。辺としても出さない
				// (画面のグラフに「行けない先」を描かないため。数だけ出す)
				if !robotsBlockedSeen[l] {
					robotsBlockedSeen[l] = true
					robotsBlocked++
				}
				continue
			}
			e := model.Link{From: r.page.URL, To: l}
			if edgeSeen[e] {
				continue // 同じ辺は一度だけ(G-12)
			}
			edgeSeen[e] = true
			links = append(links, e)
			// キューに入れるのは未知の URL だけ。上限に達していれば入れない(D-06)
			queued := false
			if !seen.Has(l) {
				if seen.Len() >= cfg.MaxPages {
					capped = true
				} else if seen.Add(l) {
					queued = true
				}
			}
			// 辺は queued でなくても流す — 画面はリンク構造(閉路・上限で切られた先)も描く(F-34)
			events <- model.Event{Type: model.EvLinkFound, T: clock(), From: r.page.URL, To: l, Queued: queued}
			if queued {
				jobs <- l
				inflight++
			}
		}
	}
	close(jobs) // Worker は range を抜けて worker_done を出す
	wg.Wait()

	reason := model.ReasonExhausted
	switch {
	case parent.Err() != nil:
		reason = model.ReasonCancelled
	case ctx.Err() != nil:
		reason = model.ReasonDeadline
	case capped:
		reason = model.ReasonMaxPages
	}
	stats := ComputeStatistics(pages, time.Since(t0))
	events <- model.Event{
		Type: model.EvCrawlCompleted, T: clock(), Reason: reason, Statistics: &stats,
		RobotsBlocked: robotsBlocked,
	}
	close(events)
	deliver.Wait()

	return model.Result{
		Status: "completed", Reason: reason, Pages: pages, Links: links, Statistics: stats,
		Robots: robotsState, RobotsBlocked: robotsBlocked,
		EffectiveDelayMs: int(cfg.RequestDelay / time.Millisecond),
		Sitemap:          sitemapState,
		SitemapURLs:      len(sitemapSeeds),
		SitemapSeeded:    sitemapAdded,
		External:         external.Domains(),
		ExternalLinks:    external.Total(),
	}
}

// fetchSitemap は種 URL を取りに行く(原本 §34 B)。
// robots.txt が Sitemap 行を持てばそれを使い、無ければ /sitemap.xml を試す。
// sitemapindex なら 1 段だけ辿る(入れ子を無限に追わない)。
func fetchSitemap(ctx context.Context, cfg Config, start string, robots *Robots) ([]string, string) {
	locations := []string{}
	if robots != nil {
		for _, s := range robots.Sitemaps {
			if SameDomain(start, s) { // 別ドメインのサイトマップは使わない
				locations = append(locations, s)
			}
		}
	}
	declared := len(locations) > 0
	if !declared {
		if base, err := url.Parse(start); err == nil {
			locations = append(locations, (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/sitemap.xml"}).String())
		}
	}

	var seeds []string
	seen := map[string]bool{}
	found := false
	// 1 段目 + index を辿る 2 段目。合わせて 5 ファイルまで
	for i := 0; i < len(locations) && i < 5; i++ {
		sm, ok := getSitemap(ctx, cfg, locations[i])
		if !ok {
			continue
		}
		found = true
		if sm.IsIndex {
			for _, nested := range sm.URLs {
				if len(locations) < 5 && SameDomain(start, nested) {
					locations = append(locations, nested)
				}
			}
			continue
		}
		for _, u := range sm.URLs {
			n, err := Normalize(u)
			if err != nil || seen[n] {
				continue
			}
			seen[n] = true
			seeds = append(seeds, n)
		}
	}
	switch {
	case !found && declared:
		return nil, model.SitemapDeclaredMissing
	case !found:
		return nil, model.SitemapAbsent
	default:
		return seeds, model.SitemapUsed
	}
}

// getSitemap は 1 ファイル取って解析する。取れなければ ok=false。
func getSitemap(ctx context.Context, cfg Config, loc string) (Sitemap, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return Sitemap{}, false
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := cfg.Client.Do(req)
	if err != nil {
		return Sitemap{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Sitemap{}, false
	}
	return ParseSitemap(io.LimitReader(resp.Body, SitemapMaxBytes)), true
}

// fetchRobots は開始 URL のホストから /robots.txt を取り、判定器と状態を返す。
// RespectRobots が偽なら取りに行かない。RFC 9309 の状態の扱い:
//
//	2xx        → 解析して従う(RobotsObeyed)
//	4xx        → 規則が無い(RobotsAbsent。すべて許可)
//	5xx / 失敗 → 到達不能(RobotsUnreachable。complete disallow)
func fetchRobots(ctx context.Context, cfg Config) (*Robots, string) {
	if !cfg.RespectRobots {
		return nil, model.RobotsIgnored
	}
	base, err := url.Parse(cfg.StartURL)
	if err != nil {
		return nil, model.RobotsUnreachable
	}
	ref := &url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/robots.txt"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.String(), nil)
	if err != nil {
		return nil, model.RobotsUnreachable
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := cfg.Client.Do(req)
	if err != nil {
		// 接続できない = 到達不能。ただし ctx のキャンセルは別物なので許可側に倒す
		// (止めたのは利用者であって、サイトの意思表示ではない)
		if ctx.Err() != nil {
			return nil, model.RobotsAbsent
		}
		return nil, model.RobotsUnreachable
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return ParseRobots(io.LimitReader(resp.Body, RobotsMaxBytes), UserAgentToken), model.RobotsObeyed
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, model.RobotsAbsent
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, model.RobotsUnreachable
	}
}

// ComputeStatistics は pages から統計を決定的に計算する(SPEC §5)。
//   - Success = Error が空、Errors = それ以外、Total = 両者の和
//   - AvgMs = DurationMs の平均(切り捨て)
//   - P95Ms = DurationMs を昇順に並べ ceil(0.95 × n) 番目(1 始まり)
//   - RequestsPerSec = Total / (elapsed 秒)
func ComputeStatistics(pages []model.Page, elapsed time.Duration) model.Statistics {
	st := model.Statistics{Total: len(pages), DurationMs: elapsed.Milliseconds()}
	if len(pages) == 0 {
		return st
	}
	durs := make([]int64, 0, len(pages))
	var sum int64
	for _, p := range pages {
		if p.Error == "" {
			st.Success++
		} else {
			st.Errors++
		}
		durs = append(durs, p.DurationMs)
		sum += p.DurationMs
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	st.AvgMs = sum / int64(len(durs))
	idx := int(math.Ceil(0.95*float64(len(durs)))) - 1
	if idx < 0 {
		idx = 0
	}
	st.P95Ms = durs[idx]
	// SPEC §5: total / (durationMs / 1000)。画面・テストが同じ式で再計算できるよう、
	// ミリ秒に丸めた後の DurationMs から出す(生の経過時間から出すと最下位桁が食い違う)
	if st.DurationMs > 0 {
		st.RequestsPerSec = math.Round(float64(st.Total)/(float64(st.DurationMs)/1000)*10) / 10
	}
	return st
}
