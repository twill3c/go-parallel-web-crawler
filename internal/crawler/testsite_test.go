package crawler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// site は合成サイト。pages[path] = そのページが張るリンク(パス)。
// 生成関数がページ集合と辺集合を返すので、期待値はここから**導出**する(定数を二重に書かない — HC-068)。
type site struct {
	pages map[string][]string
	root  string

	delay   time.Duration
	mu      sync.Mutex
	starts  []time.Time // 各リクエストの開始時刻(G-07 の判定に使う)
	cur     int32
	maxCur  int32             // 同時接続数の最大値(G-06)
	nonHTML map[string]string // path → Content-Type(非 HTML を返すパス)
	status  map[string]int    // path → ステータス
}

// treeSite は深さ・分岐で木を作る。到達可能ページ数 = 1 + b + b^2 + ... + b^depth。
// さらに「どこからもリンクされない」ページを orphans 件足す(到達不能の対照)。
func treeSite(branch, depth, orphans int) *site {
	s := &site{pages: map[string][]string{}, root: "/", nonHTML: map[string]string{}, status: map[string]int{}}
	var build func(path string, d int)
	build = func(path string, d int) {
		var kids []string
		if d < depth {
			for i := 0; i < branch; i++ {
				kid := strings.TrimSuffix(path, "/") + fmt.Sprintf("/n%d", i)
				kids = append(kids, kid)
			}
		}
		// 同じリンクを二か所に持たせる(G-12: 辺は一意)+ 親へ戻るリンク(閉路)
		links := append([]string{}, kids...)
		links = append(links, kids...)
		links = append(links, "/")
		s.pages[path] = links
		for _, k := range kids {
			build(k, d+1)
		}
	}
	build("/", 0)
	for i := 0; i < orphans; i++ {
		s.pages[fmt.Sprintf("/orphan%d", i)] = []string{"/"}
	}
	return s
}

// reachable は root から辿れるページのパス集合(BFS)。
func (s *site) reachable() map[string]bool {
	seen := map[string]bool{s.root: true}
	q := []string{s.root}
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		for _, l := range s.pages[p] {
			if _, ok := s.pages[l]; ok && !seen[l] {
				seen[l] = true
				q = append(q, l)
			}
		}
	}
	return seen
}

// edges は (from,to) の一意な辺集合(重複リンクを潰す)。
func (s *site) edges() map[[2]string]bool {
	e := map[[2]string]bool{}
	for from, links := range s.pages {
		for _, to := range links {
			e[[2]string{from, to}] = true
		}
	}
	return e
}

func (s *site) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.starts = append(s.starts, time.Now())
		s.mu.Unlock()
		c := atomic.AddInt32(&s.cur, 1)
		for {
			m := atomic.LoadInt32(&s.maxCur)
			if c <= m || atomic.CompareAndSwapInt32(&s.maxCur, m, c) {
				break
			}
		}
		defer atomic.AddInt32(&s.cur, -1)
		if s.delay > 0 {
			select {
			case <-time.After(s.delay):
			case <-r.Context().Done():
				return
			}
		}
		if ct, ok := s.nonHTML[r.URL.Path]; ok {
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(200)
			_, _ = w.Write([]byte("\x89PNG not html <a href=\"/should-not-follow\">x</a>"))
			return
		}
		if st, ok := s.status[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(st)
			_, _ = w.Write([]byte("<html><title>err</title><a href=\"/from-error-page\">x</a></html>"))
			return
		}
		links, ok := s.pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var b strings.Builder
		fmt.Fprintf(&b, "<html><head><title>Page %s</title></head><body>", r.URL.Path)
		for _, l := range links {
			fmt.Fprintf(&b, "<a href=%q>%s</a>", l, l)
		}
		b.WriteString("<a href=\"https://other.example.org/ext\">external</a></body></html>")
		_, _ = w.Write([]byte(b.String()))
	})
}

func (s *site) serve() *httptest.Server { return httptest.NewServer(s.handler()) }

// requestStarts は記録した開始時刻を昇順で返す。
func (s *site) requestStarts() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]time.Time{}, s.starts...)
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}
