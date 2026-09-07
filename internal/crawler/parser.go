package crawler

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// PageMeta はページから拾う書誌情報(ROADMAP E)。すべて任意で、無ければ空。
type PageMeta struct {
	Title       string // <title>
	Description string // <meta name="description" content="...">
	H1          string // 最初の <h1> の文字列(入れ子の要素の中身も含む)
	Canonical   string // <link rel="canonical" href="..."> を絶対化・正規化したもの
}

// ExtractPage は HTML から <a href> と書誌情報を取り出す(SPEC F-07, G-03 / ROADMAP E)。
// リンクは base(または最初の <base href>)に対して絶対化・正規化し、文書順・重複なしで返す。
//
// 捨てるもの: http/https 以外の scheme(mailto: / javascript: / tel: …)、空の href、
// フラグメントだけの href("#top")。別ドメインは**捨てない**(除外は呼び手の責務 F-06)。
// 壊れた HTML でも落ちない(x/net/html のトークナイザは寛容)。
func ExtractPage(base string, r io.Reader) (links []string, meta PageMeta) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, meta
	}
	seen := map[string]bool{}
	z := html.NewTokenizer(r)
	baseSet := false
	var canonicalRaw string
	// 文字を集めている最中の入れ物。nil でなければテキストをそこへ流す
	var sink *strings.Builder
	var titleBuf, h1Buf strings.Builder
	h1Depth := 0 // <h1> の中にいる深さ(入れ子の要素をまたいで文字を拾うため)

	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			meta.Title = squash(titleBuf.String())
			meta.H1 = squash(h1Buf.String())
			meta.Canonical = absNormalize(baseURL, canonicalRaw)
			return links, meta

		case html.TextToken:
			if sink != nil {
				sink.Write(z.Text())
			}

		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "title":
				if sink == &titleBuf {
					sink = nil
				}
			case "h1":
				if h1Depth > 0 {
					h1Depth--
					if h1Depth == 0 && sink == &h1Buf {
						sink = nil
					}
				}
			}

		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "title":
				if tt == html.StartTagToken && titleBuf.Len() == 0 {
					sink = &titleBuf
				}
			case "h1":
				if tt == html.StartTagToken && h1Buf.Len() == 0 && h1Depth == 0 {
					sink = &h1Buf
					h1Depth = 1
				} else if tt == html.StartTagToken && h1Depth > 0 {
					h1Depth++
				}
			case "meta":
				attrs := allAttrs(z, hasAttr)
				if strings.EqualFold(attrs["name"], "description") && meta.Description == "" {
					meta.Description = squash(attrs["content"])
				}
			case "link":
				attrs := allAttrs(z, hasAttr)
				if canonicalRaw == "" && hasRelToken(attrs["rel"], "canonical") {
					canonicalRaw = attrs["href"]
				}
			case "base":
				if !baseSet {
					if href, ok := attr(z, hasAttr, "href"); ok {
						if b, err := baseURL.Parse(href); err == nil {
							baseURL = b
							baseSet = true
						}
					}
				}
			case "a":
				href, ok := attr(z, hasAttr, "href")
				if !ok {
					continue
				}
				href = strings.TrimSpace(href)
				if href == "" || strings.HasPrefix(href, "#") {
					continue
				}
				abs, err := baseURL.Parse(href)
				if err != nil {
					continue
				}
				n, err := Normalize(abs.String())
				if err != nil || seen[n] {
					continue
				}
				seen[n] = true
				links = append(links, n)
			}
		}
	}
}

// ExtractLinks は ExtractPage の薄い包み(既存の呼び出しと読みやすさのために残す)。
func ExtractLinks(base string, r io.Reader) ([]string, string) {
	links, meta := ExtractPage(base, r)
	return links, meta.Title
}

// squash は前後の空白を落とし、連続する空白を 1 個に畳む。
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// absNormalize は raw を base に対して絶対化し、正規化する。空・不正なら空文字。
func absNormalize(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	abs, err := base.Parse(raw)
	if err != nil {
		return ""
	}
	n, err := Normalize(abs.String())
	if err != nil {
		return ""
	}
	return n
}

// hasRelToken は rel 属性(空白区切りの語の並び)に token が含まれるかを返す。
func hasRelToken(rel, token string) bool {
	for _, f := range strings.Fields(rel) {
		if strings.EqualFold(f, token) {
			return true
		}
	}
	return false
}

// allAttrs は現在のタグの属性をすべて map にする(属性名は小文字で返る)。
func allAttrs(z *html.Tokenizer, hasAttr bool) map[string]string {
	m := map[string]string{}
	for hasAttr {
		k, v, more := z.TagAttr()
		m[string(k)] = string(v)
		hasAttr = more
	}
	return m
}

// attr はトークナイザの現在タグから属性 key を探す。トークナイザは属性名を小文字で返す。
func attr(z *html.Tokenizer, hasAttr bool, key string) (string, bool) {
	for hasAttr {
		k, v, more := z.TagAttr()
		if string(k) == key {
			return string(v), true
		}
		hasAttr = more
	}
	return "", false
}

// StatusClass は画面が使う区分を返す(ROADMAP D)。色ではなく区分名なので、
// 記号にも文字にも割り当てられる。
//
//	ok       2xx で取れた
//	redirect 3xx(追跡した結果として記録に残る場合)
//	missing  4xx(あるべきものが無い・見せてもらえない)
//	server   5xx(相手側の障害)
//	failed   応答が無い(タイムアウト・DNS・接続失敗)
//	other    取れたが解析対象外、または中断
func StatusClass(status int, errText string) string {
	switch errText {
	case model.ErrTimeout, model.ErrDNS, model.ErrConnection,
		model.ErrTooManyRedirects, model.ErrOffDomain, model.ErrForbidden:
		return "failed"
	}
	switch {
	case status >= 200 && status < 300 && errText == "":
		return "ok"
	case status >= 300 && status < 400:
		return "redirect"
	case status >= 400 && status < 500:
		return "missing"
	case status >= 500:
		return "server"
	default:
		return "other"
	}
}
