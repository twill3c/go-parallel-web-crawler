package crawler

import (
	"context"
	"sync"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// result は Worker が manager へ返す 1 件。skipped はキャンセル後に取得せず捨てた job。
type result struct {
	page    model.Page
	links   []string
	skipped bool
}

// worker は Worker Pool の 1 本(goroutine)。
//
//	for url := range jobs {            // jobs が閉じられるまで取り出し続ける
//	    events <- worker_started
//	    <delay>                         // Request Delay(ctx で中断可・F-14)
//	    page, links := fetch(ctx, url)
//	    events <- page_completed
//	    results <- result{page, links}  // manager が集約し、新しい URL を jobs へ足す
//	}
//	events <- worker_done               // goroutine が抜けた印
//
// ctx がキャンセルされた後は取得せず、job を skipped として返す。manager の勘定
// (送った job の数 = 返ってきた result の数)を崩さないためで、これがあるから
// manager は「inflight が 0 になったら jobs を閉じる」だけで全体を畳める。
func worker(ctx context.Context, id int, jobs <-chan string, results chan<- result, events chan<- model.Event,
	fetch *Fetcher, delay time.Duration, clock func() int64, wg *sync.WaitGroup) {
	defer wg.Done()
	pages := 0
	for u := range jobs {
		if ctx.Err() != nil {
			results <- result{skipped: true}
			continue
		}
		events <- model.Event{Type: model.EvWorkerStarted, T: clock(), WorkerID: id, URL: u}
		if delay > 0 {
			// 待っている間に止められても Fetch に進む。ctx が済んでいれば Fetch は接続せずに
			// Cancelled を返すので、started に対する completed が必ず対になる(F-13)
			sleepCtx(ctx, delay)
		}
		page, links := fetch.Fetch(ctx, u)
		page.WorkerID = id
		pages++
		events <- model.Event{
			Type: model.EvPageCompleted, T: clock(), WorkerID: id, URL: u,
			StatusCode: page.StatusCode, DurationMs: page.DurationMs, Title: page.Title, Error: page.Error,
			Description: page.Description, H1: page.H1, Canonical: page.Canonical, FinalURL: page.FinalURL,
		}
		results <- result{page: page, links: links}
	}
	events <- model.Event{Type: model.EvWorkerDone, T: clock(), WorkerID: id, Pages: pages}
}

// sleepCtx は d だけ待つ。ctx が先に終わったら false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
