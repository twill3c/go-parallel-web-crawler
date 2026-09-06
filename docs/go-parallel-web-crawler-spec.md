# Go Parallel Web Crawler
## Webクローラー並列処理可視化アプリ 仕様書

### 1. 概要

#### 1.1 アプリ名

**Go Parallel Web Crawler**

#### 1.2 目的

Webサイトを指定すると、そのWebサイト内のページを自動的に巡回し、ページ間のリンク構造とクロール処理の並列実行状況をブラウザ上で可視化する。

本アプリは単なるWebクローラーではなく、Go言語の特徴である以下の技術を実際に体験できることを目的とする。

- goroutine
- channel
- Worker Pool
- context
- sync.Mutex
- net/http
- URL処理
- 並行処理
- 処理時間計測

#### 1.3 想定利用者

- Goを学習中のエンジニア
- Goのgoroutine / channelを理解したい人
- Webクローラーの仕組みを学びたい人
- 並列処理と性能の関係を体験したい人
- AIコーディングエージェントによるGo開発の教材として利用する人

#### 1.4 公開方式

GitHubリポジトリをソースコードの正本とし、VercelとGitHubを連携して自動デプロイする。

```text
Developer
   │
   ▼
GitHub
   │
   │ Push
   ▼
Vercel
   │
   ├── Frontend
   │
   └── Go API
```

---

# 2. 基本コンセプト

ユーザーがURLを入力すると、

```text
URL
 ↓
Go Crawler
 ↓
URL Queue
 ↓
Worker Pool
 ↓
複数ページを並列取得
 ↓
リンク抽出
 ↓
新しいURLをQueueへ追加
 ↓
結果集約
 ↓
ブラウザへJSON/SSEで通知
```

という処理を行う。

画面では同時に、

1. Webサイトのリンク構造
2. Workerの稼働状況
3. クロール進捗
4. 処理時間
5. 成功・失敗件数

を表示する。

---

# 3. MVPの範囲

最初のバージョンでは機能を絞る。

### MVPで実装する機能

- URL入力
- クロール開始
- 最大ページ数指定
- Worker数指定
- 同一ドメイン制限
- HTMLページ取得
- `<a href>`からリンク抽出
- 重複URL排除
- 並列クロール
- クロール結果一覧
- ページ間リンクグラフ
- Worker状態表示
- 処理時間表示
- 成功/失敗件数表示
- クロール停止
- エラー表示

### MVPでは実装しない

- ログイン
- データベース
- ユーザー履歴
- 大規模サイトの長時間クロール
- JavaScriptレンダリング
- robots.txtの完全な実装
- 外部サイトへの大量アクセス
- サイトマップ解析
- 認証が必要なページのクロール

---

# 4. 利用条件・安全対策

本アプリは学習・デモ用途の小規模クローラーとする。

### デフォルト値

```text
Workers       5
Max Pages     50
Request Delay 200ms
Timeout       5sec
```

### 上限

```text
Workers       最大20
Max Pages     最大100
Timeout       最大10秒
```

利用者が無制限にリクエストを発生させられないようにする。

原則として、

> 「自分が管理するサイト、またはクロールが許可されているサイトで利用してください」

という注意事項を画面に表示する。

---

# 5. 画面仕様

## 5.1 メイン画面

```text
┌────────────────────────────────────────────────────────┐
│              Go Parallel Web Crawler                  │
│        Visualize Go's concurrent web crawling         │
├────────────────────────────────────────────────────────┤
│ Target URL                                             │
│ [ https://example.com                         ]        │
│                                                        │
│ Workers      [──────●────────] 5                      │
│ Max Pages    [────●───────────] 50                     │
│                                                        │
│ Request Delay [ 200 ] ms                               │
│                                                        │
│ [ START CRAWL ]     [ STOP ]                           │
├──────────────────────────────┬─────────────────────────┤
│                              │                         │
│       CRAWL GRAPH            │       WORKERS           │
│                              │                         │
│          ●────●              │ W01  ● Crawling        │
│         /      \             │ W02  ● Crawling        │
│        ●        ●            │ W03  ✓ Done            │
│         \      /             │ W04  ● Crawling        │
│          ●────●              │ W05  Waiting           │
│                              │                         │
├──────────────────────────────┴─────────────────────────┤
│ STATISTICS                                             │
│                                                        │
│ Pages     42       Links       137                    │
│ Success   40       Errors        2                    │
│ Elapsed   2.83s    Requests/sec 14.8                  │
├────────────────────────────────────────────────────────┤
│ PAGE RESULTS                                           │
│                                                        │
│ URL                         Status   Time              │
│ /                          200       82ms              │
│ /about                     200       71ms              │
│ /products                  200      124ms              │
└────────────────────────────────────────────────────────┘
```

---

# 6. URL入力

### 入力項目

| 項目 | 必須 | デフォルト |
|---|---|---|
| Target URL | ○ | なし |
| Workers | ○ | 5 |
| Max Pages | ○ | 50 |
| Request Delay | ○ | 200ms |

### URL検証

以下をチェックする。

- URL形式が正しい
- schemeが`http`または`https`
- hostnameが存在する
- localhostなどの内部向けURLを拒否
- 不正なURLを拒否

---

# 7. クロール方式

## 7.1 Breadth First Search

基本的にはBFS方式でURLを探索する。

```text
Start
  │
  ├── A
  ├── B
  └── C
       │
       ├── D
       ├── E
       └── F
```

URL Queueを利用する。

```text
Queue
 ↓
Worker
 ↓
HTTP GET
 ↓
HTML解析
 ↓
リンク抽出
 ↓
未取得URLをQueueへ
```

---

# 8. Worker Pool

Goの主要機能としてWorker Poolを採用する。

概念設計：

```text
                 jobs channel
                     │
       ┌─────────────┼─────────────┐
       ↓             ↓             ↓
   Worker 1      Worker 2      Worker 3
       │             │             │
       ↓             ↓             ↓
     HTTP          HTTP          HTTP
       │             │             │
       └─────────────┼─────────────┘
                     ↓
               results channel
```

Worker数はユーザー指定値に応じて生成する。

---

# 9. Go内部構造

推奨ディレクトリ構成：

```text
go-parallel-web-crawler/
│
├── api/
│   ├── crawl.go
│   ├── events.go
│   └── health.go
│
├── internal/
│   ├── crawler/
│   │   ├── crawler.go
│   │   ├── worker.go
│   │   ├── queue.go
│   │   ├── fetcher.go
│   │   └── parser.go
│   │
│   ├── model/
│   │   └── model.go
│   │
│   └── security/
│       └── url.go
│
├── public/
│   ├── index.html
│   ├── app.js
│   └── style.css
│
├── tests/
│   └── ...
│
├── go.mod
├── go.sum
├── README.md
└── vercel.json
```

---

# 10. データモデル

## Page

```go
type Page struct {
    URL        string `json:"url"`
    StatusCode int    `json:"statusCode"`
    DurationMs int64  `json:"durationMs"`
    Title      string `json:"title"`
    Error      string `json:"error,omitempty"`
}
```

## Link

```go
type Link struct {
    From string `json:"from"`
    To   string `json:"to"`
}
```

## CrawlResult

```go
type CrawlResult struct {
    Pages       []Page `json:"pages"`
    Links       []Link `json:"links"`
    Total       int    `json:"total"`
    Success     int    `json:"success"`
    Errors      int    `json:"errors"`
    DurationMs  int64  `json:"durationMs"`
}
```

## WorkerStatus

```go
type WorkerStatus struct {
    ID        int    `json:"id"`
    Status    string `json:"status"`
    CurrentURL string `json:"currentUrl,omitempty"`
}
```

---

# 11. クロール状態

Workerには以下の状態を持たせる。

```text
waiting
 ↓
crawling
 ↓
completed
```

エラー時：

```text
crawling
 ↓
error
 ↓
waiting
```

全体の状態：

```text
idle
running
stopping
completed
error
```

---

# 12. 重複URL対策

同じURLを複数Workerが処理しないようにする。

```go
type URLSet struct {
    mu   sync.Mutex
    urls map[string]bool
}
```

URL追加時：

```text
Lock
 ↓
既に存在？
 ├─ Yes → Ignore
 └─ No  → Add
 ↓
Unlock
```

ここでは`sync.Mutex`を利用する。

これもGoの並行処理を学ぶ重要なポイントとする。

---

# 13. contextによるキャンセル

「STOP」を押した場合、現在実行中のクロールを停止する。

```text
Browser
  │
  │ STOP
  ↓
Cancel Context
  │
  ├── Worker 1 → stop
  ├── Worker 2 → stop
  ├── Worker 3 → stop
  └── Worker 4 → stop
```

Goの`context.Context`を利用する。

---

# 14. HTTP取得

Go標準の`net/http`を使用する。

処理：

```text
URL
 ↓
http.Client
 ↓
GET
 ↓
Status Code確認
 ↓
Content-Type確認
 ↓
HTML取得
```

HTML以外、

```text
image
pdf
zip
video
```

などは原則として解析対象外とする。

---

# 15. リンク抽出

HTMLから、

```html
<a href="/about">
<a href="/products">
```

などを抽出する。

相対URLを絶対URLに変換する。

```text
https://example.com
+
/about
↓
https://example.com/about
```

外部ドメインへのリンクはMVPでは除外する。

```text
example.com
 ├── /about       ○
 ├── /products    ○
 └── google.com   ×
```

---

# 16. クロールグラフ

ページをNode、リンクをEdgeとして扱う。

```text
Node = Page
Edge = Link
```

例：

```text
Home
 ├── About
 ├── Products
 │    ├── A
 │    └── B
 └── Contact
```

フロントエンドではSVGまたはCanvasを利用して描画する。

MVPではSVGベースを推奨する。

---

# 17. Worker可視化

Workerごとに状態を表示する。

```text
Worker 01  ● Crawling
           https://example.com/products

Worker 02  ● Crawling
           https://example.com/about

Worker 03  ✓ Completed

Worker 04  ● Crawling
           https://example.com/contact

Worker 05  ○ Waiting
```

色ではなくアイコン・文字でも状態を識別できるようにする。

---

# 18. リアルタイムイベント

将来的にはSSEを利用する。

イベント例：

```json
{
  "type": "worker_started",
  "workerId": 2,
  "url": "https://example.com/about"
}
```

```json
{
  "type": "page_completed",
  "workerId": 2,
  "url": "https://example.com/about",
  "statusCode": 200,
  "durationMs": 82
}
```

```json
{
  "type": "link_found",
  "from": "https://example.com",
  "to": "https://example.com/about"
}
```

```json
{
  "type": "crawl_completed"
}
```

---

# 19. API仕様

## POST /api/crawl

クロール開始。

Request：

```json
{
  "url": "https://example.com",
  "workers": 5,
  "maxPages": 50,
  "requestDelayMs": 200
}
```

Response：

```json
{
  "crawlId": "abc123"
}
```

---

## GET /api/crawl/{id}

クロール結果取得。

Response：

```json
{
  "status": "completed",
  "pages": [],
  "links": [],
  "statistics": {
    "total": 50,
    "success": 48,
    "errors": 2,
    "durationMs": 2830
  }
}
```

---

## GET /api/crawl/{id}/events

SSEによるリアルタイムイベント取得。

```text
Content-Type: text/event-stream
```

---

## POST /api/crawl/{id}/stop

クロール停止。

---

## GET /api/health

ヘルスチェック。

Response：

```json
{
  "status": "ok"
}
```

---

# 20. 性能測定

このアプリでは性能測定を重要機能とする。

表示項目：

```text
Pages
Success
Errors
Elapsed Time
Requests/sec
Average Response Time
P95 Response Time
```

特に、

```text
Workers = 1
Workers = 5
Workers = 10
Workers = 20
```

の違いを比較できるようにする。

---

# 21. Benchmark機能

将来機能として、

```text
┌──────────────────────────────┐
│       Worker Benchmark       │
├──────────────────────────────┤
│ Workers    Time     Pages/s  │
│                              │
│    1       8.21s     6.1     │
│    5       2.13s    23.5     │
│   10       1.24s    40.3     │
│   20       1.18s    42.4     │
└──────────────────────────────┘
```

を表示する。

実測値をグラフ化する。

---

# 22. エラー処理

以下を個別に扱う。

```text
Invalid URL
Connection Timeout
DNS Error
HTTP 4xx
HTTP 5xx
Non HTML
Too Many Redirects
Context Cancelled
```

画面には、

```text
Errors: 3

/aaa       404 Not Found
/bbb       Timeout
/ccc       500 Internal Server Error
```

のように表示する。

---

# 23. セキュリティ

URLを外部から受け取るため、SSRF対策を重要項目とする。

最低限、

- localhost拒否
- `127.0.0.1`拒否
- プライベートIP拒否
- loopback拒否
- link-local拒否
- 不正scheme拒否
- リダイレクト先も再検証
- リクエスト数制限
- ページ数制限
- タイムアウト設定

を行う。

---

# 24. Vercel制約への対応

Vercel上では本格的な常駐クローラーではなく、**短時間・小規模なクロール**を前提とする。

MVPでは、

```text
Max Pages = 50
Workers = 5
```

を標準設定とする。

最大値も小さく抑える。

大量クロールや長時間処理が必要になった場合は、将来的に別のWorker実行基盤へ切り離せる設計とする。

---

# 25. データベース

MVPでは使用しない。

```text
Browser
   ↓
Vercel
   ↓
Go Function
   ↓
Memory
```

クロール終了後にデータを永続保存しない。

これにより、

- DB料金不要
- DB管理不要
- APIキー不要
- 初期構築が簡単

というメリットを得る。

---

# 26. フロントエンド

MVPではReactなどを必須としない。

以下を基本構成とする。

```text
HTML
CSS
Vanilla JavaScript
```

理由：

- 構成が単純
- Vercelへの公開が容易
- Go APIとの関係が理解しやすい
- AIエージェントでコード全体を把握しやすい
- ビルド環境を最小化できる

必要になった場合のみTypeScript/Reactなどへ移行する。

---

# 27. テスト方針

TDDを基本とする。

## Unit Test

対象：

```text
URL正規化
URL重複判定
同一ドメイン判定
相対URL変換
リンク抽出
Worker状態遷移
統計計算
```

例：

```text
TestNormalizeURL
TestIsSameDomain
TestExtractLinks
TestDuplicateURL
TestWorkerStatus
```

---

# 28. Integration Test

以下を確認する。

```text
POST /api/crawl
 ↓
Go Crawler
 ↓
Worker Pool
 ↓
HTTP取得
 ↓
結果
```

テスト用のローカルHTTPサーバーを用意し、外部サイトに依存しないテストを行う。

---

# 29. 開発フェーズ

## Phase 1：最小クローラー

```text
URL入力
 ↓
GET
 ↓
HTML取得
 ↓
リンク抽出
```

---

## Phase 2：Worker Pool

```text
Queue
 ↓
Worker × N
 ↓
並列取得
```

Goのgoroutine/channelを導入する。

---

## Phase 3：重複排除

```text
URLSet
+
Mutex
```

を実装する。

---

## Phase 4：API化

```text
/api/crawl
/api/health
```

を実装。

---

## Phase 5：UI

- URL入力
- Start
- Stop
- Worker表示
- Statistics
- Page一覧

を実装。

---

## Phase 6：Graph

ページとリンクをグラフとして表示する。

---

## Phase 7：リアルタイム化

SSEを導入。

```text
Go Worker
 ↓
Event
 ↓
SSE
 ↓
Browser
```

---

## Phase 8：Benchmark

Worker数による性能差を可視化する。

---

# 30. 完成版の機能構成

```text
                 Go Parallel Web Crawler
                           │
        ┌──────────────────┼──────────────────┐
        │                  │                  │
        ▼                  ▼                  ▼
   URL Crawler        Worker Pool        Statistics
        │                  │                  │
        │             goroutine              │
        │             channel                │
        │             context                │
        │                  │                  │
        └──────────────────┼──────────────────┘
                           ▼
                    Crawl Results
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
          Page List     Graph View    Worker View
```

---

# 31. READMEで説明する技術テーマ

GitHubのREADMEには、単なる使い方だけでなく、

### What can you learn?

```text
✓ goroutine
✓ channel
✓ Worker Pool
✓ sync.Mutex
✓ context cancellation
✓ HTTP client
✓ URL parsing
✓ concurrent processing
✓ performance measurement
```

という学習項目を掲載する。

---

# 32. AIエージェント開発を意識したドキュメント

このプロジェクトでは、以下のMarkdownをGitHubに配置する。

```text
README.md
SPEC.md
ARCHITECTURE.md
ROADMAP.md
TESTCASE.md
SECURITY.md
```

特に、

```text
SPEC.md
ARCHITECTURE.md
TESTCASE.md
ROADMAP.md
```

をAIコーディングエージェントの参照資料として利用する。

AIエージェントには、

```text
SPEC.mdを仕様の正本とする。
実装前にARCHITECTURE.mdを確認する。
TESTCASE.mdのテストを追加・更新する。
ROADMAP.mdの未完了タスクから作業する。
```

というルールを与える。

---

# 33. Definition of Done

MVP完成条件：

- [ ] GitHubからcloneできる
- [ ] Goプロジェクトとしてビルドできる
- [ ] Vercelへデプロイできる
- [ ] URLを入力できる
- [ ] クロール開始できる
- [ ] 同一ドメインをクロールできる
- [ ] 最大ページ数を制御できる
- [ ] Worker数を制御できる
- [ ] goroutineで並列処理している
- [ ] channelを使用してWorkerとManagerを連携している
- [ ] 重複URLを排除できる
- [ ] Stopで処理をキャンセルできる
- [ ] ページ一覧を表示できる
- [ ] リンクグラフを表示できる
- [ ] Worker状態を表示できる
- [ ] 処理時間を表示できる
- [ ] エラーを表示できる
- [ ] Unit Testが存在する
- [ ] READMEが完成している
- [ ] SSRF対策を実装している

---

# 34. 将来拡張

完成後は以下を追加できる。

### A. robots.txt対応

```text
robots.txt
     ↓
Crawler Policy
     ↓
許可されたURLだけ取得
```

### B. サイトマップ対応

```text
/sitemap.xml
```

からURLを取得する。

### C. 外部ドメイン可視化

```text
example.com
     │
     ├── github.com
     ├── wikipedia.org
     └── example.org
```

### D. リンク切れ検出

```text
200 ✓
301 →
404 ✕
500 !
```

### E. SEO情報分析

- title
- description
- h1
- canonical
- status code

などを表示する。

### F. ページ速度分析

各ページの取得時間を比較する。

### G. Go vs TypeScript比較

同一条件で、

```text
Go
vs
TypeScript
```

の処理時間を測定する。

ただし結果は環境・ネットワークなどに依存するため、ベンチマーク結果を一般的な言語性能の結論として扱わない。

---

# 35. 最終的なコンセプト

このアプリの価値は、

> 「Webサイトをクロールできます」

ではなく、

> **「Goのgoroutineとchannelによる並行処理が、実際のWebクローリングでどのように動いているのかを目で見て理解できる」**

ことにある。

そのため、完成版では**クロール結果だけでなく、Workerが何をしているかを可視化することを最重要UIとする。**

最終的なキャッチコピー：

**Go Parallel Web Crawler**  
*See Go concurrency in action.*

---

## 推奨技術スタック

| 項目 | 採用技術 |
|---|---|
| Backend | Go |
| HTTP | `net/http` |
| Concurrency | goroutine / channel |
| Synchronization | `sync.Mutex` |
| Cancellation | `context` |
| Frontend | HTML / CSS / JavaScript |
| Graph | SVG |
| Communication | JSON / SSE |
| Test | Go testing |
| Source Control | Git / GitHub |
| Hosting | Vercel |
| Database | なし |
| External API | なし |
| Cost | 無料枠前提 |