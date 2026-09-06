package crawler

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// T-106 / F-07, G-03: リンク抽出の表。期待値の出所: SPEC G-03 と RFC 3986 §5(相対参照の解決)。
// 自作フィクスチャなので、各行が主張したい性質を持つことをコメントに明示する(HC-068)。
func TestExtractLinks_Table(t *testing.T) {
	base := "https://example.com/dir/page.html"
	html := `<!doctype html><html><head><title> Hello &amp; World </title></head><body>
<a href="/about">相対(ルート)</a>
<a href="contact.html">相対(同階層) → /dir/contact.html</a>
<a href="../up">相対(親) → /up</a>
<a href="https://example.com/abs">絶対</a>
<a href="//example.com/proto">スキーム相対 → https</a>
<a href="https://other.example.org/x">別ドメイン(抽出はする。除外は呼び手 F-06)</a>
<a href="mailto:a@example.com">mailto は捨てる</a>
<a href="javascript:void(0)">javascript は捨てる</a>
<a href="tel:+81">tel は捨てる</a>
<a href="#top">フラグメントだけは捨てる</a>
<a href="">空は捨てる</a>
<a>href なし</a>
<A HREF="/UPPER">大文字タグ・属性</A>
<a href='/single'>単引用符</a>
<a href="/about">重複 → 一度だけ</a>
<a href="/q?b=2&amp;a=1#x">クエリは保つ・フラグメントは落とす</a>
</body></html>`

	got, title := ExtractLinks(base, strings.NewReader(html))
	if title != "Hello & World" {
		t.Errorf("title = %q, want %q", title, "Hello & World")
	}
	want := []string{
		"https://example.com/about",
		"https://example.com/dir/contact.html",
		"https://example.com/up",
		"https://example.com/abs",
		"https://example.com/proto",
		"https://other.example.org/x",
		"https://example.com/UPPER",
		"https://example.com/single",
		"https://example.com/q?b=2&a=1",
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("links mismatch\n got=%v\nwant=%v", got, want)
	}
}

// T-106 補 / G-03: <base href> があれば相対参照はそれに対して解決する(最初の <base> だけ効く)。
func TestExtractLinks_BaseHref(t *testing.T) {
	html := `<html><head><base href="https://example.com/sub/"><base href="https://ignored.example.com/"></head>
<body><a href="a.html">x</a><a href="/root">y</a></body></html>`
	got, _ := ExtractLinks("https://example.com/other/page", strings.NewReader(html))
	want := []string{"https://example.com/sub/a.html", "https://example.com/root"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
}

// 抽出器は文書順を保つ(BFS の到達順がグラフ描画の順になる)。
func TestExtractLinks_PreservesOrder(t *testing.T) {
	html := `<a href="/c">c</a><a href="/a">a</a><a href="/b">b</a>`
	got, _ := ExtractLinks("https://example.com/", strings.NewReader(html))
	want := []string{"https://example.com/c", "https://example.com/a", "https://example.com/b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
}

// 陰性対照: リンクの無い文書・壊れた HTML でも落ちない(空集合・title 空)。
func TestExtractLinks_Empty(t *testing.T) {
	got, title := ExtractLinks("https://example.com/", strings.NewReader("<p>no links <a href=\"</p>"))
	if len(got) != 0 || title != "" {
		t.Errorf("got=%v title=%q, want empty", got, title)
	}
}
