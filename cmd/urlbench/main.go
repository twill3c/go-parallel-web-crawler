// cmd/urlbench は URL 正規化と同一ドメイン判定だけを回す(ROADMAP G の測定 2)。
// bench/urlbench.ts と**同じ入力・同じ回数**を回し、同じ形の JSON を出す。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/crawler"
)

type out struct {
	Lang    string  `json:"lang"`
	Runtime string  `json:"runtime"`
	N       int     `json:"n"`
	Ms      float64 `json:"ms"`
	NsPerOp int64   `json:"nsPerOp"`
	OK      int     `json:"ok"`
	Same    int     `json:"same"`
	Sink    int     `json:"sink"`
}

func main() {
	n := flag.Int("n", 100000, "回数")
	flag.Parse()

	// bench/urlbench.ts と同じ入力を、同じ規則で作る
	hosts := []string{"example.com", "www.example.com", "Example.COM", "sub.example.com", "example.org"}
	paths := []string{"/", "/a", "/a/b/../c", "/a?x=1#frag", "/%E6%97%A5%E6%9C%AC/x", "/deep/path/to/page.html"}
	inputs := make([]string, 0, *n)
	for i := 0; i < *n; i++ {
		scheme := "https"
		if i%3 == 0 {
			scheme = "http"
		}
		port := ""
		if i%7 == 0 {
			if scheme == "http" {
				port = ":80"
			} else {
				port = ":443"
			}
		}
		inputs = append(inputs, fmt.Sprintf("%s://%s%s%s", scheme, hosts[i%len(hosts)], port, paths[i%len(paths)]))
	}
	const start = "https://example.com/"

	// TS 側と同じだけ暖機を回す(Go には JIT が無いが、条件を揃えるため同じ手順を踏む)
	sink := 0
	warm := *n / 10
	for i := 0; i < warm; i++ {
		if _, err := crawler.Normalize(inputs[i]); err == nil {
			sink++
		}
	}

	t0 := time.Now()
	ok, same := 0, 0
	for i := 0; i < *n; i++ {
		u, err := crawler.Normalize(inputs[i])
		if err == nil {
			ok++
			if crawler.SameDomain(start, u) {
				same++
			}
		}
	}
	el := time.Since(t0)

	o := out{
		Lang:    "go",
		Runtime: fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH),
		N:       *n,
		Ms:      float64(el.Microseconds()) / 1000,
		NsPerOp: el.Nanoseconds() / int64(*n),
		OK:      ok, Same: same, Sink: sink,
	}
	if err := json.NewEncoder(os.Stdout).Encode(o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
