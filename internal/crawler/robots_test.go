package crawler

import (
	"strings"
	"testing"
	"time"
)

// T-701 / G-14: 群の選択(RFC 9309 §2.2.1)。
// 期待値の出所: RFC 9309 の規範文 —— 「大文字小文字を無視して一致する群を探す」
// 「複数一致したら結合する」「一致が無ければ * の群に従う」「* も無ければ規則は適用されない」。
func TestParseRobots_GroupSelection(t *testing.T) {
	body := `
User-agent: *
Disallow: /everyone

User-agent: GoParallelWebCrawler
User-agent: OtherBot
Disallow: /ours

User-agent: gOpArAlLeLwEbCrAwLeR
Disallow: /ours2

User-agent: Unrelated
Disallow: /
`
	// 自分の名前で引くと、大文字小文字違いの群も含めて結合される。* の群は使わない
	r := ParseRobots(strings.NewReader(body), "GoParallelWebCrawler")
	for _, c := range []struct {
		path string
		want bool
	}{
		{"/ours", false},
		{"/ours2", false},
		{"/everyone", true}, // * の群は自分に一致する群がある限り使わない
		{"/other", true},
	} {
		if got := r.Allows(c.path); got != c.want {
			t.Errorf("Allows(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	// 一致する群が無ければ * の群に従う
	other := ParseRobots(strings.NewReader(body), "SomeoneElse")
	if other.Allows("/everyone") {
		t.Error("* の群が適用されていない")
	}
	if !other.Allows("/ours") {
		t.Error("自分宛でない群が適用された")
	}
	// * も自分もない → 規則は適用されない(すべて許可)
	none := ParseRobots(strings.NewReader("User-agent: Unrelated\nDisallow: /\n"), "Me")
	if !none.Allows("/anything") {
		t.Error("一致する群も * も無いのに拒否された")
	}
}

// T-702 / G-14: 最長一致と同長時の Allow 優先(RFC 9309 §2.2.2)。
func TestParseRobots_LongestMatch(t *testing.T) {
	r := ParseRobots(strings.NewReader(`
User-agent: *
Disallow: /a/
Allow: /a/b/
Disallow: /a/b/c/
Allow: /x
Disallow: /x
`), "Me")
	cases := []struct {
		path string
		want bool
		why  string
	}{
		{"/", true, "規則に当たらない"},
		{"/a/", false, "Disallow /a/ (4 octets)"},
		{"/a/z", false, "Disallow /a/ が唯一の一致"},
		{"/a/b/", true, "Allow /a/b/ (6) が Disallow /a/ (4) より長い"},
		{"/a/b/z", true, "同上"},
		{"/a/b/c/", false, "Disallow /a/b/c/ (8) が最長"},
		{"/a/b/c/d", false, "同上"},
		{"/x", true, "同長なら Allow を採る"},
		{"/xyz", true, "同上(前方一致)"},
	}
	for _, c := range cases {
		if got := r.Allows(c.path); got != c.want {
			t.Errorf("Allows(%q) = %v, want %v (%s)", c.path, got, c.want, c.why)
		}
	}
}

// T-703 / G-14: ワイルドカード * と終端 $(RFC 9309 §2.2.3)。
func TestParseRobots_Wildcards(t *testing.T) {
	r := ParseRobots(strings.NewReader(`
User-agent: *
Disallow: /*.pdf$
Disallow: /private*/secret
Allow: /docs/*/public
Disallow: /docs/
`), "Me")
	cases := []struct {
		path string
		want bool
	}{
		{"/a.pdf", false},
		{"/deep/path/file.pdf", false},
		{"/a.pdf.html", true}, // $ が末尾を要求する
		{"/private1/secret", false},
		{"/private/secret", false}, // * は 0 文字にも当たる
		{"/privateX/secretY", false},
		{"/docs/", false},
		{"/docs/a/public", true}, // Allow のほうが長い
		{"/docs/a/private", false},
	}
	for _, c := range cases {
		if got := r.Allows(c.path); got != c.want {
			t.Errorf("Allows(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// T-704: 構文の細部 —— 空行で群が切れる、コメント、前後の空白、未知のフィールド、
// 値の無い Disallow(= すべて許可)、大文字小文字が違うフィールド名。
func TestParseRobots_Syntax(t *testing.T) {
	r := ParseRobots(strings.NewReader(`
# コメント行
USER-AGENT: me    # 行内コメント
DISALLOW:   /blocked
Sitemap: https://example.com/sitemap.xml
Unknown-Field: 何か
Disallow:
Crawl-delay: 2.5
`), "Me")
	if r.Allows("/blocked") {
		t.Error("Disallow が効いていない")
	}
	if !r.Allows("/other") {
		t.Error("値の無い Disallow がすべてを拒否している")
	}
	if r.CrawlDelay != 2500*time.Millisecond {
		t.Errorf("CrawlDelay = %v, want 2.5s", r.CrawlDelay)
	}
	if len(r.Sitemaps) != 1 || r.Sitemaps[0] != "https://example.com/sitemap.xml" {
		t.Errorf("Sitemaps = %v", r.Sitemaps)
	}
	// 群の切れ目: 空行を挟むと別の群になる
	two := ParseRobots(strings.NewReader("User-agent: me\nDisallow: /a\n\nUser-agent: other\nDisallow: /b\n"), "me")
	if two.Allows("/a") || !two.Allows("/b") {
		t.Error("空行で群が切れていない")
	}
}

// T-705 / G-14: パスの照合は percent-encoding を揃えてから行う。
// 期待値の出所: RFC 9309 §2.2.2「オクテット単位で比較する」。robots.txt 側と URL 側で
// 符号化が違うと同じ資源が別物になるので、両方を復号してから比べる。
func TestParseRobots_PercentEncoding(t *testing.T) {
	r := ParseRobots(strings.NewReader("User-agent: *\nDisallow: /%E6%97%A5%E6%9C%AC/\nDisallow: /a%20b\n"), "Me")
	for _, p := range []string{"/日本/x", "/%E6%97%A5%E6%9C%AC/x", "/a b", "/a%20b"} {
		if r.Allows(p) {
			t.Errorf("Allows(%q) = true, want false", p)
		}
	}
	if !r.Allows("/日本語") { // 前方一致なので / で終わる規則には当たらない
		t.Error("関係ないパスが拒否された")
	}
}

// T-706: 空の robots.txt・規則の無い群はすべて許可(RFC 9309「規則が無ければ許可」)。
func TestParseRobots_Empty(t *testing.T) {
	for _, body := range []string{"", "\n\n", "# comment only\n", "User-agent: *\n"} {
		r := ParseRobots(strings.NewReader(body), "Me")
		if !r.Allows("/anything") {
			t.Errorf("body=%q: 拒否された", body)
		}
		if r.CrawlDelay != 0 {
			t.Errorf("body=%q: CrawlDelay = %v", body, r.CrawlDelay)
		}
	}
}

// T-707 / G-14 陽性対照: 検査器が実際に撃つことを確かめる。
// 「すべて拒否」の robots.txt を与えたとき、Allows が false を返さなければ検査は何も見ていない。
func TestParseRobots_PositiveControl(t *testing.T) {
	r := ParseRobots(strings.NewReader("User-agent: *\nDisallow: /\n"), "Me")
	for _, p := range []string{"/", "/a", "/a/b/c?q=1"} {
		if r.Allows(p) {
			t.Fatalf("Disallow: / が効いていない(%q)", p)
		}
	}
}
