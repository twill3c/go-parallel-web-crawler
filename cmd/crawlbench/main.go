// cmd/crawlbench は Go 版クローラを 1 回走らせて結果を JSON で出す(ROADMAP G の比較用)。
// 出荷物ではなく、bench/run.mjs から呼ばれる測定の道具である。
//
//	go run ./cmd/crawlbench -url http://127.0.0.1:1234/ -workers 5 -max 30 -delay 0
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/crawler"
	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

type out struct {
	Lang    string `json:"lang"`
	WallMs  int64  `json:"wallMs"`
	Pages   int    `json:"pages"`
	Links   int    `json:"links"`
	Reason  string `json:"reason"`
	FetchMs int64  `json:"fetchMs"` // 取得に費やした時間の総和(各ページの durationMs の和)
	Success int    `json:"success"`
	Errors  int    `json:"errors"`
	// FirstError は最初に起きたエラーの種別(比較が成立しなかったときの手がかり)
	FirstError string `json:"firstError,omitempty"`
}

func main() {
	url := flag.String("url", "", "開始 URL")
	workers := flag.Int("workers", 5, "Worker 数")
	maxPages := flag.Int("max", 30, "最大ページ数")
	delay := flag.Int("delay", 0, "Request Delay(ms)")
	flag.Parse()
	if *url == "" {
		fmt.Fprintln(os.Stderr, "-url is required")
		os.Exit(2)
	}

	cfg := crawler.Config{
		StartURL:     *url,
		Workers:      *workers,
		MaxPages:     *maxPages,
		RequestDelay: time.Duration(*delay) * time.Millisecond,
		Timeout:      10 * time.Second,
		Deadline:     5 * time.Minute,
		// 合成サイトは 127.0.0.1 なので SSRF 検査を外す。robots.txt は取りに行かない
		// (TS 版も取りに行かないので、比較の条件を揃えるため)
		RespectRobots: false,
	}
	cfg.Client = crawler.NewClient(cfg.StartURL, crawler.ClientOptions{Timeout: cfg.Timeout, AllowPrivate: true})

	t0 := time.Now()
	res := crawler.Run(context.Background(), cfg, func(model.Event) {})
	wall := time.Since(t0)

	var fetchMs int64
	firstErr := ""
	for _, p := range res.Pages {
		fetchMs += p.DurationMs
		if firstErr == "" && p.Error != "" {
			firstErr = p.Error
		}
	}
	o := out{
		Lang: "go", WallMs: wall.Milliseconds(), Pages: len(res.Pages), Links: len(res.Links),
		Reason: res.Reason, FetchMs: fetchMs,
		Success: res.Statistics.Success, Errors: res.Statistics.Errors, FirstError: firstErr,
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
