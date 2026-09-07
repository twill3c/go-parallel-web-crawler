package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
)

// smOptions は合成サイトの設定。**サイトマップは 1 か所にしか置かない**ので、
// 「robots の Sitemap 行を使えたか」を後退経路(/sitemap.xml)と区別して測れる。
type smOptions struct {
	robots      string // BASE がサイト自身の URL に置き換わる。空なら robots.txt は 404
	sitemapPath string // サイトマップを置くパス(既定 /sitemap.xml)
	sitemapCode int    // そのパスの応答コード(既定 200)
	sitemapBody string // BASE が置き換わる
	otherSMCode int    // 上記以外の /sitemap* への応答(既定 404 = 後退経路を塞ぐ)
}

// smSite は「トップからは辿れないページ」を持つ合成サイト。
// サイトマップがあればそこへ到達でき、無ければ到達できない —— これが B の効きを測る対照になる。
func smSite(t *testing.T, o smOptions) *httptest.Server {
	t.Helper()
	if o.sitemapPath == "" {
		o.sitemapPath = "/sitemap.xml"
	}
	if o.sitemapCode == 0 {
		o.sitemapCode = 200
	}
	if o.otherSMCode == 0 {
		o.otherSMCode = 404
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch {
		case r.URL.Path == "/robots.txt":
			if o.robots == "" {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, strings.ReplaceAll(o.robots, "BASE", base))
		case r.URL.Path == o.sitemapPath:
			if o.sitemapCode != 200 {
				w.WriteHeader(o.sitemapCode)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, strings.ReplaceAll(o.sitemapBody, "BASE", base))
		case strings.HasPrefix(r.URL.Path, "/sitemap"):
			w.WriteHeader(o.otherSMCode) // 後退経路
		default:
			w.Header().Set("Content-Type", "text/html")
			// トップは /linked だけを指す。/orphan1, /orphan2 はどこからも指されない
			if r.URL.Path == "/" {
				fmt.Fprint(w, `<title>root</title><a href="/linked">l</a>`+
					`<a href="https://github.com/x">gh</a><a href="https://github.com/y">gh2</a>`+
					`<a href="https://ja.wikipedia.org/z">wiki</a>`)
				return
			}
			fmt.Fprintf(w, `<title>%s</title><a href="/">home</a><a href="https://github.com/x">gh</a>`, r.URL.Path)
		}
	}))
	_ = srv
	t.Cleanup(srv.Close)
	return srv
}

func runSM(t *testing.T, srv *httptest.Server, useSitemap bool) model.Result {
	t.Helper()
	cfg := Config{
		StartURL: srv.URL + "/", Workers: 2, MaxPages: 20,
		Timeout: 2 * time.Second, RespectRobots: true, UseSitemap: useSitemap,
	}
	cfg.Client = NewClient(cfg.StartURL, ClientOptions{Timeout: cfg.Timeout, AllowPrivate: true})
	return Run(context.Background(), cfg, func(model.Event) {})
}

const smBody = `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
<url><loc>BASE/orphan1</loc></url><url><loc>BASE/orphan2</loc></url><url><loc>BASE/</loc></url>
<url><loc>https://other.example.org/x</loc></url></urlset>`

func hasPage(res model.Result, suffix string) bool {
	for _, p := range res.Pages {
		if strings.HasSuffix(p.URL, suffix) {
			return true
		}
	}
	return false
}

// T-1111 / ROADMAP-B: サイトマップの URL が種になり、トップから辿れないページに到達する。
// **対照**: 同じサイトを UseSitemap=false で回すと到達しない。
func TestRun_SitemapSeeds(t *testing.T) {
	srv := smSite(t, smOptions{sitemapBody: smBody})

	with := runSM(t, srv, true)
	without := runSM(t, srv, false)

	if !hasPage(with, "/orphan1") || !hasPage(with, "/orphan2") {
		t.Errorf("サイトマップの URL に到達していない: %d ページ", len(with.Pages))
	}
	// 対照が効いていること(効いていなければ、この検査は何も言っていない)
	if hasPage(without, "/orphan1") {
		t.Fatal("サイトマップを使わない設定でも到達した —— 対照が成立していない")
	}
	if with.Sitemap != model.SitemapUsed || without.Sitemap != model.SitemapSkipped {
		t.Errorf("状態 = %q / %q", with.Sitemap, without.Sitemap)
	}
	// SitemapURLs は**サイトマップが挙げていた URL の数**(4 件)。同一ドメインの絞り込みは種に入れる段で行う
	if with.SitemapURLs != 4 {
		t.Errorf("SitemapURLs = %d, want 4", with.SitemapURLs)
	}
	// 種に入るのは orphan 2 件だけ(開始 URL は既知・別ドメインは落ちる)
	if with.SitemapSeeded != 2 {
		t.Errorf("SitemapSeeded = %d, want 2", with.SitemapSeeded)
	}
}

// T-1112 / ROADMAP-B: robots.txt の Sitemap 行を使う。
// **後退経路(/sitemap.xml)は 404 にしてある**ので、robots の行が効かなければ到達できない。
func TestRun_SitemapFromRobots(t *testing.T) {
	srv := smSite(t, smOptions{
		robots:      "User-agent: *\nSitemap: BASE/sitemap-a.xml\n",
		sitemapPath: "/sitemap-a.xml",
		sitemapBody: smBody,
	})
	// 前提の検算: 後退経路が塞がっている(ここが 200 なら、この検査は robots の行を見ていない)
	resp, err := http.Get(srv.URL + "/sitemap.xml")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("/sitemap.xml = %d, want 404(後退経路が開いていると対照にならない)", resp.StatusCode)
	}

	res := runSM(t, srv, true)
	if res.Sitemap != model.SitemapUsed {
		t.Fatalf("状態 = %q, want used", res.Sitemap)
	}
	if res.SitemapSeeded != 2 || !hasPage(res, "/orphan1") {
		t.Errorf("robots の Sitemap 行から種を取れていない: seeded=%d", res.SitemapSeeded)
	}
}

// T-1113 / ROADMAP-B: サイトマップが無いときは absent。宣言されているのに取れなければ declared_missing。
func TestRun_SitemapMissing(t *testing.T) {
	absent := runSM(t, smSite(t, smOptions{sitemapCode: 404, sitemapBody: smBody}), true)
	if absent.Sitemap != model.SitemapAbsent {
		t.Errorf("状態 = %q, want absent", absent.Sitemap)
	}
	if len(absent.Pages) == 0 {
		t.Error("サイトマップが無いだけでクロールが止まった")
	}

	declared := smSite(t, smOptions{
		robots:      "User-agent: *\nSitemap: BASE/sitemap-a.xml\n",
		sitemapPath: "/sitemap-a.xml",
		sitemapCode: 500,
		sitemapBody: smBody,
	})
	res := runSM(t, declared, true)
	if res.Sitemap != model.SitemapDeclaredMissing {
		t.Errorf("状態 = %q, want declared_missing", res.Sitemap)
	}
	if len(res.Pages) == 0 {
		t.Error("サイトマップが取れないだけでクロールが止まった")
	}
}

// T-1114 / ROADMAP-C: 外部ドメインを辿らずに数える。
func TestRun_ExternalTally(t *testing.T) {
	res := runSM(t, smSite(t, smOptions{sitemapCode: 404, sitemapBody: smBody}), false)
	// 辿っていないこと
	for _, p := range res.Pages {
		if strings.Contains(p.URL, "github.com") || strings.Contains(p.URL, "wikipedia.org") {
			t.Fatalf("外部ドメインを辿った: %s", p.URL)
		}
	}
	// 数えていること(合成サイトは必ず外部リンクを出すので、0 なら集計が働いていない)
	if res.ExternalLinks == 0 {
		t.Fatal("外部リンクが 0 —— 集計が働いていない")
	}
	byHost := map[string]model.ExternalDomainCount{}
	for _, d := range res.External {
		byHost[d.Host] = d
	}
	gh, ok := byHost["github.com"]
	if !ok {
		t.Fatalf("github.com が集計に無い: %+v", res.External)
	}
	if gh.Links != 2 {
		t.Errorf("github.com の一意な URL = %d, want 2(/x と /y)", gh.Links)
	}
	if gh.FromPages < 2 {
		t.Errorf("github.com の参照元ページ = %d, want 2 以上(トップと /linked)", gh.FromPages)
	}
	if _, ok := byHost["ja.wikipedia.org"]; !ok {
		t.Errorf("ja.wikipedia.org が無い(サブドメインは畳まない): %+v", res.External)
	}
	// 多い順に並ぶ
	for i := 1; i < len(res.External); i++ {
		if res.External[i-1].Links < res.External[i].Links {
			t.Errorf("並び順が件数の降順でない: %+v", res.External)
		}
	}
}
