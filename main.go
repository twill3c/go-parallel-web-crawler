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
	mux := http.NewServeMux()
	api.Register(mux)
	mux.Handle("/", http.FileServer(http.Dir("public")))

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
