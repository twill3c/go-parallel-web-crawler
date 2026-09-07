package crawler

import (
	"strings"
	"testing"
)

// T-1101 / ROADMAP-B: sitemap.xml の <loc> を取り出す。
// 期待値の出所: sitemaps.org の書式(<urlset><url><loc>)。名前空間の有無に関わらず読めること。
func TestParseSitemap_Urlset(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/a</loc><lastmod>2026-01-01</lastmod></url>
  <url><loc> https://example.com/b </loc></url>
  <url><loc>https://example.com/a</loc></url>
  <url><loc>not a url</loc></url>
  <url><loc>mailto:x@example.com</loc></url>
  <url></url>
</urlset>`
	sm := ParseSitemap(strings.NewReader(body))
	if sm.IsIndex {
		t.Error("urlset を index と判定した")
	}
	want := []string{"https://example.com/a", "https://example.com/b"}
	if len(sm.URLs) != len(want) {
		t.Fatalf("URLs = %v, want %v(空白を落とし・重複を畳み・不正を捨てる)", sm.URLs, want)
	}
	for i, w := range want {
		if sm.URLs[i] != w {
			t.Errorf("URLs[%d] = %q, want %q", i, sm.URLs[i], w)
		}
	}
}

// T-1102 / ROADMAP-B: sitemapindex は入れ子の sitemap の場所を返す。
func TestParseSitemap_Index(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/sitemap-1.xml</loc></sitemap>
  <sitemap><loc>https://example.com/sitemap-2.xml</loc></sitemap>
</sitemapindex>`
	sm := ParseSitemap(strings.NewReader(body))
	if !sm.IsIndex {
		t.Fatal("sitemapindex を index と判定していない")
	}
	if len(sm.URLs) != 2 || sm.URLs[0] != "https://example.com/sitemap-1.xml" {
		t.Errorf("URLs = %v", sm.URLs)
	}
}

// T-1103: 壊れた XML・空・HTML を渡しても落ちず、空を返す(陰性対照)。
func TestParseSitemap_Broken(t *testing.T) {
	for _, body := range []string{"", "<urlset>", "<html><body>not xml</body></html>", "\x00\x01"} {
		sm := ParseSitemap(strings.NewReader(body))
		if len(sm.URLs) != 0 {
			t.Errorf("body=%q: URLs = %v, want 空", body, sm.URLs)
		}
	}
}

// T-1104 / ROADMAP-B: 読み取り上限。巨大な sitemap でも SitemapMaxURLs で打ち切る。
func TestParseSitemap_Cap(t *testing.T) {
	var b strings.Builder
	b.WriteString("<urlset>")
	for i := 0; i < SitemapMaxURLs+50; i++ {
		b.WriteString("<url><loc>https://example.com/p")
		b.WriteString(strings.Repeat("0", 1))
		b.WriteString(itoa(i))
		b.WriteString("</loc></url>")
	}
	b.WriteString("</urlset>")
	sm := ParseSitemap(strings.NewReader(b.String()))
	if len(sm.URLs) != SitemapMaxURLs {
		t.Errorf("URLs = %d, want %d(上限で打ち切る)", len(sm.URLs), SitemapMaxURLs)
	}
	if !sm.Truncated {
		t.Error("打ち切ったのに Truncated が false")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for i > 0 {
		d = append([]byte{byte('0' + i%10)}, d...)
		i /= 10
	}
	return string(d)
}

// T-1105 / ROADMAP-C: 外部ドメインの集計。ホストごとに件数と参照元ページ数を数える。
func TestExternalTally(t *testing.T) {
	tally := NewExternalTally()
	tally.Add("https://example.com/", "https://github.com/a")
	tally.Add("https://example.com/", "https://github.com/b")
	tally.Add("https://example.com/x", "https://github.com/a") // 別ページから同じ URL
	// ホストは www の有無を畳んで 1 つに数える。ただし **URL としては別物**
	// (scheme が違えば別の資源。混在していることは見えたほうがよい)
	tally.Add("https://example.com/x", "http://WWW.Wikipedia.org/z")
	tally.Add("https://example.com/x", "https://wikipedia.org/z")
	// 大文字小文字・既定ポート・フラグメントだけの違いは畳む(Normalize してから数える)
	tally.Add("https://example.com/x", "https://WIKIPEDIA.org:443/z#top")

	got := tally.Domains()
	if len(got) != 2 {
		t.Fatalf("ドメイン数 = %d(%v), want 2", len(got), got)
	}
	// 多い順に並ぶ
	if got[0].Host != "github.com" {
		t.Errorf("先頭 = %q, want github.com", got[0].Host)
	}
	if got[0].Links != 2 || got[0].FromPages != 2 {
		t.Errorf("github.com: 一意な URL %d(want 2)・参照元ページ %d(want 2)", got[0].Links, got[0].FromPages)
	}
	// http と https で 2 URL。3 件目は正規化すると 2 件目と同じになるので増えない
	if got[1].Host != "wikipedia.org" || got[1].Links != 2 || got[1].FromPages != 1 {
		t.Errorf("wikipedia.org = %+v(scheme 違いで 2 URL・参照元は 1 ページ)", got[1])
	}
	if tally.Total() != 4 {
		t.Errorf("外部リンクの総数(一意な URL)= %d, want 4", tally.Total())
	}
	// 陰性対照: 何も足さなければ空
	if len(NewExternalTally().Domains()) != 0 {
		t.Error("空の集計がドメインを返した")
	}
}
