// cmd/benchsite は比較用の合成サイトを Go で立てる(ROADMAP G の測り直し)。
//
// **なぜ Go で書き直したか。** 最初は Node で書いていたが、遅延 0 の条件では
// 1 スレッドのサーバが先に飽和し、**クローラでなくサーバを測っていた**
// (Go の Workers を 5 にしても 1 に対して 2.7 倍しか出なかった)。
// Go なら複数のコアで捌けるので、受け側が律速になりにくい。
//
// bench/site.mjs と**同じ HTML** を返す(同じページ数・同じリンクの張り方・同じ本文長)。
// 出荷物ではない。bench/run.mjs から起動される測定の道具である。
//
//	go run ./cmd/benchsite -pages 400 -delay 0 -bytes 4000 -links 8
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	pages := flag.Int("pages", 30, "ページ数")
	delay := flag.Int("delay", 0, "1 応答あたりの遅延 ms")
	bytesPer := flag.Int("bytes", 4000, "本文の目安バイト数")
	links := flag.Int("links", 8, "1 ページから張るリンクの本数")
	flag.Parse()

	// 本文は起動時に組んでおく(応答ごとに組むと、サーバ側の費用が測定に混ざる)
	filler := "<p>" + strings.Repeat("あ", max(0, *bytesPer/3)) + "</p>"
	bodies := make([][]byte, *pages+1)
	for i := 0; i <= *pages; i++ {
		var b strings.Builder
		fmt.Fprintf(&b, "<!doctype html><html><head><title>Page %d</title></head><body>", i)
		for k := 1; k <= *links; k++ {
			t := (i+k*7)%*pages + 1
			fmt.Fprintf(&b, `<a href="/p%d">to %d</a>`, t, t)
		}
		b.WriteString(`<a href="/">home</a><a href="https://other.example.org/x">ext</a><a href="/asset.png">img</a>`)
		b.WriteString(filler)
		b.WriteString("</body></html>")
		bodies[i] = []byte(b.String())
	}

	var hits int64
	d := time.Duration(*delay) * time.Millisecond
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		i := 0
		if strings.HasPrefix(r.URL.Path, "/p") {
			if n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/p")); err == nil && n >= 0 && n <= *pages {
				i = n
			}
		}
		body := bodies[i]
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// 起動を知らせる。呼び手はこの 1 行を読んでから測定を始める
	out, _ := json.Marshal(map[string]string{"url": "http://" + ln.Addr().String() + "/"})
	fmt.Println(string(out))
	os.Stdout.Sync()

	srv := &http.Server{Handler: mux}
	_ = srv.Serve(ln)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
