// Go Parallel Web Crawler — See Go concurrency in action.
//
// ルートの main.go が PORT で待ち受ける通常の net/http サーバ(SPEC D-01: Vercel の Go Framework Preset)。
// API は internal/api、クローラ本体は internal/crawler、画面は public/ の静的ファイル。
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/twill3c/go-parallel-web-crawler/internal/api"
)

func main() {
	s := api.NewServer()
	// 実ブラウザ検品(tests/ui/browser.test.mjs)のためだけのフラグ。ローカルの合成サイト(127.0.0.1)を
	// クロールさせるために SSRF 検査を外す。本番の環境変数には決して置かない(SPEC §2.4)。
	if os.Getenv("GPWC_ALLOW_PRIVATE") == "1" {
		s.AllowPrivate = true
		log.Printf("WARNING: GPWC_ALLOW_PRIVATE=1 — SSRF checks are disabled (test only)")
	}
	mux := http.NewServeMux()
	s.Register(mux)
	mux.Handle("/", http.FileServer(http.Dir("public")))

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
