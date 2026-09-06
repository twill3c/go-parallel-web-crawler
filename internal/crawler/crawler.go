package crawler

import (
	"context"
	"math"
	"net/http"
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

	events <- model.Event{
		Type: model.EvCrawlStarted, T: 0, CrawlID: cfg.CrawlID, URL: start,
		Workers: cfg.Workers, MaxPages: cfg.MaxPages, RequestDelayMs: int(cfg.RequestDelay / time.Millisecond),
		StartedAt: t0.UTC().Format(time.RFC3339Nano),
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
	capped := false
	inflight := 0

	seen.Add(start)
	jobs <- start
	inflight++

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
			if !SameDomain(start, l) || LooksNonHTML(l) {
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
	events <- model.Event{Type: model.EvCrawlCompleted, T: clock(), Reason: reason, Statistics: &stats}
	close(events)
	deliver.Wait()

	return model.Result{Status: "completed", Reason: reason, Pages: pages, Links: links, Statistics: stats}
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
