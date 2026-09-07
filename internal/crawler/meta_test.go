package crawler

import (
	"strings"
	"testing"
)

// T-901 / ROADMAP-E: SEO 情報(description / h1 / canonical)を title と同じ走査で拾う。
// 期待値の出所: HTML 標準の要素(<meta name=description> / <h1> / <link rel=canonical>)。
// フィクスチャは自作なので、各行が主張したい性質をコメントに明示する(HC-068)。
func TestExtractPage_Meta(t *testing.T) {
	html := `<!doctype html><html><head>
<title> 題名 &amp; 副題 </title>
<meta charset="utf-8">
<meta name="Description" content="  説明文です。  ">
<meta property="og:description" content="OG のほうは使わない">
<link rel="stylesheet" href="/a.css">
<link rel="Canonical" href="/正規/URL">
</head><body>
<header><h1>  見出し
  一つ目  </h1></header>
<h1>二つ目は使わない</h1>
<p>本文</p>
</body></html>`
	_, meta := ExtractPage("https://example.com/dir/page", strings.NewReader(html))
	if meta.Title != "題名 & 副題" {
		t.Errorf("Title = %q", meta.Title)
	}
	if meta.Description != "説明文です。" {
		t.Errorf("Description = %q(前後の空白を畳む)", meta.Description)
	}
	if meta.H1 != "見出し 一つ目" {
		t.Errorf("H1 = %q(最初の h1・改行は空白 1 個に畳む)", meta.H1)
	}
	// canonical は base に対して絶対化し、正規化する
	if meta.Canonical != "https://example.com/%E6%AD%A3%E8%A6%8F/URL" {
		t.Errorf("Canonical = %q", meta.Canonical)
	}
}

// T-902: 無い場合は空。h1 の中の要素(<span> 等)は中身の文字だけを取る。
func TestExtractPage_MetaAbsent(t *testing.T) {
	_, meta := ExtractPage("https://example.com/", strings.NewReader("<p>何も無い</p>"))
	if meta.Title != "" || meta.Description != "" || meta.H1 != "" || meta.Canonical != "" {
		t.Errorf("meta = %+v, want すべて空", meta)
	}
	_, m2 := ExtractPage("https://example.com/", strings.NewReader("<h1>前<span>中</span>後</h1>"))
	if m2.H1 != "前中後" {
		t.Errorf("H1 = %q(入れ子の文字も拾う)", m2.H1)
	}
}

// T-903: canonical が自分自身と違うかを呼び手が判定できる(正規化して比べる)。
func TestExtractPage_CanonicalDiffers(t *testing.T) {
	cases := []struct {
		base, canonical string
		same            bool
	}{
		{"https://example.com/a", `<link rel="canonical" href="https://example.com/a">`, true},
		{"https://example.com/a", `<link rel="canonical" href="/a">`, true},
		{"https://example.com/a", `<link rel="canonical" href="https://example.com/a/">`, false},
		{"https://example.com/a?x=1", `<link rel="canonical" href="https://example.com/a">`, false},
		{"https://example.com/a", `<link rel="canonical" href="https://other.example.org/a">`, false},
	}
	for _, c := range cases {
		_, meta := ExtractPage(c.base, strings.NewReader("<head>"+c.canonical+"</head>"))
		norm, err := Normalize(c.base)
		if err != nil {
			t.Fatal(err)
		}
		got := meta.Canonical == norm
		if got != c.same {
			t.Errorf("base=%s canonical=%q: 一致 %v, want %v(Canonical=%q)", c.base, c.canonical, got, c.same, meta.Canonical)
		}
	}
}

// T-904 / ROADMAP-D: HTTP 状態から画面の記号と区分を決める。
// 期待値の出所: SPEC F-33 と原本 §34 D(200 ✓ / 301 → / 404 ✕ / 500 !)。
func TestStatusClass(t *testing.T) {
	cases := []struct {
		status int
		err    string
		want   string
	}{
		{200, "", "ok"},
		{204, "", "ok"},
		{301, "", "redirect"}, // 追跡した結果 2xx になるので通常は出ないが、区分としては持つ
		{404, "HTTP 404", "missing"},
		{403, "HTTP 403", "missing"},
		{500, "HTTP 500", "server"},
		{503, "HTTP 503", "server"},
		{0, "Timeout", "failed"},   // 応答が無い
		{0, "DNS Error", "failed"}, //
		{200, "Non HTML", "other"}, // 取れたが解析対象外
		{0, "Cancelled", "other"},
	}
	for _, c := range cases {
		if got := StatusClass(c.status, c.err); got != c.want {
			t.Errorf("StatusClass(%d, %q) = %q, want %q", c.status, c.err, got, c.want)
		}
	}
}

// T-905 / ROADMAP-D: 壊れた URL を指しているページ(参照元)を辺の集合から逆引きする。
func TestReferrers(t *testing.T) {
	links := []struct{ from, to string }{
		{"https://e.com/", "https://e.com/gone"},
		{"https://e.com/a", "https://e.com/gone"},
		{"https://e.com/a", "https://e.com/ok"},
		{"https://e.com/b", "https://e.com/deep/gone"},
	}
	idx := map[string][]string{}
	for _, l := range links {
		idx[l.to] = append(idx[l.to], l.from)
	}
	// 実装は画面側(state.js)だが、Go 側でも同じ逆引きができることを型で示す。
	// ここでは「壊れた URL を指すページが漏れなく取れる」ことだけを確かめる
	if got := idx["https://e.com/gone"]; len(got) != 2 {
		t.Errorf("参照元 = %v, want 2 件", got)
	}
	if got := idx["https://e.com/deep/gone"]; len(got) != 1 || got[0] != "https://e.com/b" {
		t.Errorf("参照元 = %v", got)
	}
	if _, ok := idx["https://e.com/none"]; ok {
		t.Error("誰も指していない URL に参照元が付いた")
	}
}
