package crawler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/twill3c/go-parallel-web-crawler/internal/model"
	"github.com/twill3c/go-parallel-web-crawler/internal/security"
)

// MaxBodyBytes は応答本文の読取上限(SPEC §2.5: 2 MiB)。
const MaxBodyBytes = 2 << 20

// MaxRedirects はリダイレクトを追う回数の上限(SPEC §2.5)。
const MaxRedirects = 5

// UserAgent はクロール先に名乗る文字列。
const UserAgent = "GoParallelWebCrawler/0.1 (+https://github.com/twill3c/go-parallel-web-crawler)"

var (
	errTooManyRedirects = errors.New("too many redirects")
	errOffDomain        = errors.New("redirect off-domain")
)

// ClientOptions は NewClient の設定。AllowPrivate はテスト(httptest = 127.0.0.1)のためだけにある。
type ClientOptions struct {
	Timeout      time.Duration
	AllowPrivate bool
}

// NewClient はクロール 1 回分の http.Client を作る。
//   - 接続直前に相手 IP を再検査する(security.DialControl・DNS リバインディング対策)
//   - リダイレクトは MaxRedirects 回まで、先が同一ドメイン(§2.3)かつ内部向けでないことを再検証する
//   - タイムアウトは接続から本文読取までの全体に掛かる
func NewClient(startURL string, opt ClientOptions) *http.Client {
	dialer := &net.Dialer{Timeout: opt.Timeout, KeepAlive: 30 * time.Second}
	if !opt.AllowPrivate {
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			return security.DialControl(network, address)
		}
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   opt.Timeout,
		ResponseHeaderTimeout: opt.Timeout,
		MaxIdleConnsPerHost:   20,
		Proxy:                 nil, // 環境のプロキシを使わない(SSRF の抜け道になりうる)
	}
	return &http.Client{
		Timeout:   opt.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= MaxRedirects {
				return errTooManyRedirects
			}
			if !opt.AllowPrivate {
				if bad, _ := security.IsForbiddenHost(req.URL.Hostname()); bad {
					return security.ErrForbidden
				}
			}
			if !SameDomain(startURL, req.URL.String()) {
				return errOffDomain
			}
			return nil
		},
	}
}

// Fetcher は 1 URL を取得してページ記録とリンクを返す。
type Fetcher struct {
	Client *http.Client
}

// Fetch は url を GET し、Page(記録)と抽出したリンクを返す。
// 2xx かつ HTML のときだけ本文を解析する。エラー種別は model の定数(SPEC §5)。
func (f *Fetcher) Fetch(ctx context.Context, pageURL string) (model.Page, []string) {
	page := model.Page{URL: pageURL}
	start := time.Now()
	finish := func() { page.DurationMs = time.Since(start).Milliseconds() }

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		finish()
		page.Error = model.ErrConnection
		return page, nil
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")

	resp, err := f.Client.Do(req)
	if err != nil {
		finish()
		page.Error = classify(ctx, err)
		return page, nil
	}
	defer resp.Body.Close()
	page.StatusCode = resp.StatusCode

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		finish()
		page.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return page, nil
	}
	if !isHTML(resp.Header.Get("Content-Type")) {
		finish()
		page.Error = model.ErrNonHTML
		return page, nil
	}
	// リダイレクトで最終 URL が変わっていれば、相対リンクはそこを基準に解決する
	base := pageURL
	if resp.Request != nil && resp.Request.URL != nil {
		base = resp.Request.URL.String()
	}
	links, meta := ExtractPage(base, io.LimitReader(resp.Body, MaxBodyBytes))
	finish()
	if ctx.Err() != nil { // 本文読取の途中で止められた
		page.Error = model.ErrCancelled
		return page, nil
	}
	page.Title = meta.Title
	page.Description = meta.Description
	page.H1 = meta.H1
	page.Canonical = meta.Canonical
	// 追跡の結果 URL が変わったなら残す(ROADMAP D: 転送されたことを画面に出す)
	if n, err := Normalize(base); err == nil && n != pageURL {
		page.FinalURL = n
	}
	return page, links
}

// classify は Client.Do のエラーを §5 の種別に写す。
func classify(ctx context.Context, err error) string {
	if ctx.Err() == context.Canceled {
		return model.ErrCancelled
	}
	switch {
	case errors.Is(err, errTooManyRedirects):
		return model.ErrTooManyRedirects
	case errors.Is(err, errOffDomain):
		return model.ErrOffDomain
	case errors.Is(err, security.ErrForbidden):
		return model.ErrForbidden
	case errors.Is(err, context.DeadlineExceeded):
		return model.ErrTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return model.ErrDNS
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return model.ErrTimeout
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return model.ErrTimeout
	}
	return model.ErrConnection
}

// isHTML は Content-Type が text/html または application/xhtml+xml かを返す。
func isHTML(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// Content-Type 無しは HTML として扱わない(Non HTML)。ただし空文字だけは古いサーバのために許す
		return strings.TrimSpace(contentType) == ""
	}
	return mt == "text/html" || mt == "application/xhtml+xml"
}

// skipExtensions は明らかに HTML でない拡張子。これを持つ URL はキューに入れない(F-08)。
var skipExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".webp": true, ".ico": true,
	".pdf": true, ".zip": true, ".gz": true, ".tgz": true, ".tar": true, ".rar": true, ".7z": true,
	".mp4": true, ".mp3": true, ".wav": true, ".avi": true, ".mov": true, ".webm": true,
	".css": true, ".js": true, ".mjs": true, ".json": true, ".xml": true, ".rss": true, ".atom": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".exe": true, ".dmg": true, ".apk": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
}

// LooksNonHTML は URL のパス末尾の拡張子から非 HTML と見なせるかを返す。
func LooksNonHTML(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	if i := strings.LastIndex(p, "."); i >= 0 && !strings.Contains(p[i:], "/") {
		return skipExtensions[p[i:]]
	}
	return false
}
