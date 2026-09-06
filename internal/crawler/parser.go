package crawler

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// ExtractLinks は HTML から <a href> を抜き出し、base(または最初の <base href>)に対して
// 絶対化・正規化した URL を文書順・重複なしで返す(SPEC F-07, G-03)。<title> も返す。
//
// 捨てるもの: http/https 以外の scheme(mailto: / javascript: / tel: …)、空の href、
// フラグメントだけの href("#top")。別ドメインは**捨てない**(除外は呼び手の責務 F-06)。
// 壊れた HTML でも落ちない(x/net/html のトークナイザは寛容)。
func ExtractLinks(base string, r io.Reader) (links []string, title string) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, ""
	}
	seen := map[string]bool{}
	z := html.NewTokenizer(r)
	baseSet := false
	inTitle := false
	var titleBuf strings.Builder
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return links, strings.Join(strings.Fields(titleBuf.String()), " ")
		case html.TextToken:
			if inTitle {
				titleBuf.Write(z.Text())
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "title" {
				inTitle = false
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "title":
				inTitle = tt == html.StartTagToken && titleBuf.Len() == 0
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
