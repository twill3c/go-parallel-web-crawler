package crawler

import (
	"encoding/xml"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// サイトマップ(原本 §34 B)と外部ドメインの集計(原本 §34 C)。
//
// どちらも**同一ドメイン制限を緩めない**。サイトマップは種 URL を足すだけで、
// 別ドメインの URL は manager 側で従来どおり弾かれる。外部ドメインは数えるだけで辿らない。

// SitemapMaxURLs は 1 つのサイトマップから取り込む URL の上限。
// sitemaps.org は 1 ファイル 50,000 件までを許すが、このアプリの上限は 100 ページなので、
// それを大きく超えて読む意味が無い(読取自体の負荷も避ける)。
const SitemapMaxURLs = 1000

// SitemapMaxBytes はサイトマップの読取上限。
const SitemapMaxBytes = 4 << 20

// Sitemap は解析したサイトマップ。IsIndex なら URLs は入れ子のサイトマップの場所。
type Sitemap struct {
	URLs      []string
	IsIndex   bool
	Truncated bool // 上限で打ち切ったか
}

// ParseSitemap は sitemap.xml / sitemapindex.xml を読み、<loc> を順に取り出す。
// 空白は落とし、重複は畳み、http/https でないものと壊れたものは捨てる。
// 壊れた XML でも落ちず、読めたところまでを返す(部分的に読めるほうが役に立つ)。
func ParseSitemap(r io.Reader) Sitemap {
	var out Sitemap
	seen := map[string]bool{}
	dec := xml.NewDecoder(io.LimitReader(r, SitemapMaxBytes))
	dec.Strict = false

	inLoc := false
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break // io.EOF も構文エラーも「そこまで」で扱う
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch local(t.Name.Local) {
			case "sitemapindex":
				out.IsIndex = true
			case "loc":
				inLoc = true
				buf.Reset()
			}
		case xml.CharData:
			if inLoc {
				buf.Write(t)
			}
		case xml.EndElement:
			if local(t.Name.Local) != "loc" || !inLoc {
				continue
			}
			inLoc = false
			raw := strings.TrimSpace(buf.String())
			if raw == "" {
				continue
			}
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				continue
			}
			if seen[raw] {
				continue
			}
			if len(out.URLs) >= SitemapMaxURLs {
				out.Truncated = true
				return out
			}
			seen[raw] = true
			out.URLs = append(out.URLs, raw)
		}
	}
	return out
}

// local は名前空間つきの要素名から局所名を取り出す(xml.Name.Local は既に局所名だが、
// 名前空間が壊れている文書では "ns:loc" のまま来ることがある)。
func local(name string) string {
	if i := strings.LastIndex(name, ":"); i >= 0 {
		return name[i+1:]
	}
	return strings.ToLower(name)
}

// ExternalDomain は外部ドメイン 1 件の集計(原本 §34 C)。model と同じ形。
type ExternalDomain = model.ExternalDomainCount

// ExternalTally は外部ドメインへの参照を数える。**辿らない。数えるだけ。**
type ExternalTally struct {
	urls  map[string]map[string]bool // host → 一意な URL
	pages map[string]map[string]bool // host → 参照元ページ
}

func NewExternalTally() *ExternalTally {
	return &ExternalTally{urls: map[string]map[string]bool{}, pages: map[string]map[string]bool{}}
}

// Add は from ページから to(外部 URL)への参照を 1 件数える。
//
// ホストは小文字化し、先頭の www. を落として畳む(同一ドメイン判定と同じ流儀)。
// URL のほうは Normalize してから数える —— 大文字小文字・既定ポート・フラグメントの違いで
// 件数が水増しされないようにするため。**scheme の違いは畳まない**: http と https は別の資源で、
// 混在していることは見えたほうがよい。
func (t *ExternalTally) Add(from, to string) {
	if n, err := Normalize(to); err == nil {
		to = n
	}
	u, err := url.Parse(to)
	if err != nil || u.Host == "" {
		return
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "" {
		return
	}
	if t.urls[host] == nil {
		t.urls[host] = map[string]bool{}
		t.pages[host] = map[string]bool{}
	}
	t.urls[host][to] = true
	t.pages[host][from] = true
}

// Domains は件数の多い順(同数ならホスト名順)に返す。
func (t *ExternalTally) Domains() []ExternalDomain {
	out := make([]ExternalDomain, 0, len(t.urls))
	for host, urls := range t.urls {
		out = append(out, ExternalDomain{Host: host, Links: len(urls), FromPages: len(t.pages[host])})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Links != out[j].Links {
			return out[i].Links > out[j].Links
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// Total は外部への一意な URL の総数。
func (t *ExternalTally) Total() int {
	n := 0
	for _, urls := range t.urls {
		n += len(urls)
	}
	return n
}
