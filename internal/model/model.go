// Package model はクローラと API と画面が共有するデータ型(SPEC §5)。
package model

// Page は取得を試みた 1 ページの記録。Error が空なら成功(2xx かつ HTML)。
type Page struct {
	URL        string `json:"url"`
	StatusCode int    `json:"statusCode"`
	DurationMs int64  `json:"durationMs"`
	Title      string `json:"title"`
	Error      string `json:"error,omitempty"`
	WorkerID   int    `json:"workerId"`
	// 書誌情報(ROADMAP E)。無ければ空
	Description string `json:"description,omitempty"`
	H1          string `json:"h1,omitempty"`
	Canonical   string `json:"canonical,omitempty"`
	// FinalURL はリダイレクトを追った結果の URL(URL と同じなら空)
	FinalURL string `json:"finalUrl,omitempty"`
}

// エラー種別(SPEC §5 / 原本 §22)。画面はこの文字列をそのまま出す。
const (
	ErrTimeout          = "Timeout"
	ErrDNS              = "DNS Error"
	ErrNonHTML          = "Non HTML"
	ErrTooManyRedirects = "Too Many Redirects"
	ErrCancelled        = "Cancelled"
	ErrOffDomain        = "Redirect off-domain"
	ErrForbidden        = "Forbidden"
	ErrConnection       = "Connection Error"
)

// Link はページ間の辺。両端とも正規化後の URL。
type Link struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Statistics はクロール全体の計量(F-12)。
type Statistics struct {
	Total          int     `json:"total"`
	Success        int     `json:"success"`
	Errors         int     `json:"errors"`
	DurationMs     int64   `json:"durationMs"`
	RequestsPerSec float64 `json:"requestsPerSec"`
	AvgMs          int64   `json:"avgMs"`
	P95Ms          int64   `json:"p95Ms"`
}

// 完了理由(crawl_completed.reason)。
const (
	ReasonExhausted = "exhausted" // 辿れる URL が尽きた
	ReasonMaxPages  = "max_pages" // 上限に達し、まだ未取得の URL があった
	ReasonCancelled = "cancelled" // STOP / 切断
	ReasonDeadline  = "deadline"  // クロール全体の上限時間
	ReasonRobots    = "robots"    // robots.txt が到達不能(RFC 9309: complete disallow)で始めなかった
)

// robots.txt の状態(crawl_started.robots)。
const (
	RobotsObeyed      = "obeyed"      // 取得できて規則に従っている
	RobotsAbsent      = "absent"      // 4xx = 規則が無い(すべて許可)
	RobotsUnreachable = "unreachable" // 5xx / 接続失敗 = complete disallow
	RobotsIgnored     = "ignored"     // 利用者が「従わない」を選んだ
)

// サイトマップの状態(crawl_started.sitemap・原本 §34 B)。
const (
	SitemapUsed            = "used"             // 取得して種 URL に使った
	SitemapAbsent          = "absent"           // /sitemap.xml が無かった(宣言もされていない)
	SitemapDeclaredMissing = "declared_missing" // robots.txt が Sitemap を宣言しているのに取れなかった
	SitemapSkipped         = "skipped"          // 利用者が使わない設定にした
)

// Event は SSE で流す 1 件(SPEC §5 の表)。Type ごとに使うフィールドが違い、使わないものは省く。
type Event struct {
	Type string `json:"type"`
	T    int64  `json:"t"` // クロール開始からの経過 ms

	// crawl_started
	CrawlID        string `json:"crawlId,omitempty"`
	Workers        int    `json:"workers,omitempty"`
	MaxPages       int    `json:"maxPages,omitempty"`
	RequestDelayMs int    `json:"requestDelayMs,omitempty"`
	StartedAt      string `json:"startedAt,omitempty"`
	Robots         string `json:"robots,omitempty"`       // robots.txt の状態(上の定数)
	CrawlDelayMs   int    `json:"crawlDelayMs,omitempty"` // robots.txt の Crawl-delay(あれば)
	Sitemap        string `json:"sitemap,omitempty"`      // サイトマップの状態(上の定数)
	SitemapURLs    int    `json:"sitemapUrls,omitempty"`  // サイトマップが挙げていた URL の数

	// worker_started / page_completed / worker_done
	WorkerID    int    `json:"workerId,omitempty"`
	URL         string `json:"url,omitempty"`
	StatusCode  int    `json:"statusCode,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	Title       string `json:"title,omitempty"`
	Error       string `json:"error,omitempty"`
	Pages       int    `json:"pages,omitempty"`
	Description string `json:"description,omitempty"`
	H1          string `json:"h1,omitempty"`
	Canonical   string `json:"canonical,omitempty"`
	FinalURL    string `json:"finalUrl,omitempty"`

	// link_found
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Queued bool   `json:"queued,omitempty"` // この辺の先がキューに入ったか(既知・上限超過なら false)

	// crawl_completed
	Reason        string      `json:"reason,omitempty"`
	Statistics    *Statistics `json:"statistics,omitempty"`
	RobotsBlocked int         `json:"robotsBlocked,omitempty"` // robots.txt で辿らなかった URL の数
}

// イベント種別。
const (
	EvCrawlStarted   = "crawl_started"
	EvWorkerStarted  = "worker_started"
	EvPageCompleted  = "page_completed"
	EvLinkFound      = "link_found"
	EvExternalFound  = "external_found" // 別ドメインへのリンク(辿らない・数えるだけ)
	EvWorkerDone     = "worker_done"
	EvCrawlCompleted = "crawl_completed"
)

// Result はクロール完了時のまとめ(GET /api/crawl/{id} の本体)。
type Result struct {
	Status     string     `json:"status"`
	Reason     string     `json:"reason"`
	Pages      []Page     `json:"pages"`
	Links      []Link     `json:"links"`
	Statistics Statistics `json:"statistics"`
	// Robots は robots.txt の状態、RobotsBlocked は規則で辿らなかった URL の数、
	// EffectiveDelayMs は実際に使った Request Delay(robots.txt の Crawl-delay を下限に取る)
	Robots           string `json:"robots,omitempty"`
	RobotsBlocked    int    `json:"robotsBlocked"`
	EffectiveDelayMs int    `json:"effectiveDelayMs"`
	// サイトマップ(原本 §34 B)。URLs は挙がっていた数、Seeded は実際に種として入れた数
	Sitemap       string `json:"sitemap,omitempty"`
	SitemapURLs   int    `json:"sitemapUrls"`
	SitemapSeeded int    `json:"sitemapSeeded"`
	// 外部ドメイン(原本 §34 C)。**辿っていない。数えただけ**
	External      []ExternalDomainCount `json:"external"`
	ExternalLinks int                   `json:"externalLinks"`
}

// ExternalDomainCount は外部ドメイン 1 件の集計(crawler.ExternalDomain と同じ形)。
type ExternalDomainCount struct {
	Host      string `json:"host"`
	Links     int    `json:"links"`
	FromPages int    `json:"fromPages"`
}
