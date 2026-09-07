# SPEC.md — go-parallel-web-crawler

<!-- scaffold template v1.30.0 から展開(2026-09-06)。以後このファイルはプロジェクトが育てる -->

原本の構想は [docs/go-parallel-web-crawler-spec.md](docs/go-parallel-web-crawler-spec.md)(35 節)。
本書はそれを**要求 ID 付きに写し、実測で決まった値と設計判断を上書きしていく**正本である。
原本と本書が食い違うときは本書が勝つ(食い違いは §7 に理由つきで残す)。

## 1. 目的

URL を一つ渡すと、そのサイト内のページを幅優先で巡回し、**Go の goroutine / channel / Worker Pool /
context / sync.Mutex が実際のクロールでどう動いているか**をブラウザで見せる。
価値は「クロールできる」ことではなく、**Worker が今なにをしているかが見える**ことにある。
学習・デモ用途の小規模クローラであり、常駐しない・保存しない・外部 API を呼ばない。

キャッチコピー: **Go Parallel Web Crawler — See Go concurrency in action.**

## 2. 機能要求

### 2.1 クロール本体(Go)

| ID | 要求 | 優先度 | 原本 |
|---|---|---|---|
| F-01 | 開始 URL・Workers・Max Pages・Request Delay を受け取りクロールを開始する | must | §6, §19 |
| F-02 | URL を検証する: 形式・scheme(http/https のみ)・hostname あり・内部向け(§2.4)を拒否 | must | §6, §23 |
| F-03 | 幅優先(BFS)で探索する。URL キューは **buffered channel**(`jobs`)で表す | must | §7 |
| F-04 | Worker Pool: 指定数の goroutine が `jobs` から取り出し `results` channel へ返す | must | §8 |
| F-05 | 同一 URL を二度取得しない。`URLSet`(`sync.Mutex` + map)で排他する | must | §12 |
| F-06 | 同一ドメイン制限: 開始 URL のホストと一致するリンクだけを辿る(§2.3 の規則) | must | §15 |
| F-07 | HTML を `net/http` で取得し、`<a href>` を抽出、相対 URL を絶対化する | must | §14, §15 |
| F-08 | 非 HTML(Content-Type が text/html / application/xhtml+xml 以外)は解析せず「Non HTML」として記録。拡張子が明らかに非 HTML(画像・PDF・圧縮・動画・音声・CSS/JS/JSON/XML・フォント・実行形式・Office 文書。一覧は `fetcher.go` の `skipExtensions`)のリンクは**取得せずキューにも辺にも入れない**(実装 L2) | must | §14, §22 |
| F-09 | STOP で `context` をキャンセルし、全 Worker が停止する | must | §13 |
| F-10 | Max Pages に達したらそれ以上取得しない(取得数 ≤ Max Pages) | must | §4 |
| F-11 | 各ページの status / 所要時間 / title / エラー種別を記録する | must | §10, §22 |
| F-11b | あわせて**書誌情報**(`description` / 最初の `h1` / `canonical`)と、リダイレクトを追った場合の**最終 URL** を記録する。`canonical` は絶対化・正規化して、その URL 自身との異同が言える形にする(L9 実装) | could | §34 E |
| F-12 | 統計: Pages / Success / Errors / Elapsed / Requests/sec / 平均 / P95 を出す | must | §20 |
| F-13 | Worker の状態遷移 waiting → crawling → (error →) waiting → completed を追跡し、イベントで通知する | must | §11, §17 |
| F-14 | 各 Worker はリクエストごとに Request Delay だけ待つ(ctx で中断可) | must | §4 |
| F-15 | クロール全体に上限時間(§2.5)を置き、超えたら `deadline` 理由で完了する | must | §24 |
| F-16 | **robots.txt に従う(RFC 9309・L7 実装)。** 開始前に `/robots.txt` を 1 回取得し、product token `GoParallelWebCrawler` で群を選ぶ(大文字小文字を無視・複数一致は結合・一致が無ければ `*`)。Allow/Disallow は最長一致、同長なら Allow。`*` は 0 個以上の任意文字、末尾 `$` は終端。判定はパス+クエリに対して行い、percent-encoding は両側を復号して揃える | should | §34 A |
| F-17 | robots.txt の応答による分岐(RFC 9309): 2xx = 従う(`obeyed`)/ 4xx = 規則なし・すべて許可(`absent`)/ **5xx・接続失敗 = 全面拒否**(`unreachable`。1 ページも取らず `reason: robots` で完了)。`Crawl-delay` は Request Delay の**下限**として効かせる(利用者の値が大きければそちらを使う)。読取上限 512 KiB | should | §34 A |
| F-18 | 利用者は robots.txt を「従わない」に外せる(`ignoreRobots`)。**既定は従う** —— 欄の無い要求も従う側になる。画面は外した状態を明示する | should | — |

### 2.2 API

| ID | 要求 | 優先度 | 原本 |
|---|---|---|---|
| F-20 | `GET /api/health` → `{"status":"ok"}` | must | §19 |
| F-21 | `POST /api/crawl`(JSON: url / workers / maxPages / requestDelayMs)→ **SSE ストリーム**(`text/event-stream`)。クロールの進行をイベントとして流し、最後に `crawl_completed` を送って閉じる | must | §18, §19 |
| F-22 | 入力の範囲外は 400 で JSON `{"error": ...}`。境界は §2.5 の表 | must | §4, §6 |
| F-23 | `POST /api/crawl/{id}/stop` → 進行中クロールの context をキャンセル | should | §19 |
| F-24 | `GET /api/crawl/{id}` → 完了済み結果(JSON)。同一プロセス内のメモリのみ・保存しない | should | §19, §25 |
| F-25 | イベント種別: `crawl_started` / `worker_started` / `page_completed` / `link_found` / `worker_done` / `crawl_completed`。各 payload は §5 のスキーマ | must | §18 |

`F-23` / `F-24` は Vercel の実行形態(§7 判断 D-01)では**同じインスタンスに当たったときだけ**効く。
UI は STOP で「`/stop` を呼ぶ **かつ** fetch を abort する」の二経路を取り、どちらでも
サーバ側の `context` が cancel される(F-09)。

### 2.3 同一ドメインの規則

- ホストは小文字化し、既定ポート(http:80 / https:443)を落として比べる
- 開始 URL のホストと**完全一致**、または **`www.` の有無だけが違う**ホストを同一と見なす
- それ以外(サブドメイン・別ドメイン)は辿らない。`link_found` にも出さない(F-06)

### 2.4 SSRF 対策(内部向け URL の拒否)

| 拒否対象 | 判定 |
|---|---|
| `localhost` / `*.localhost` / `*.local` / `*.internal` | ホスト名で拒否 |
| ループバック(127/8, ::1) / プライベート(10/8, 172.16/12, 192.168/16, fc00::/7) / リンクローカル(169.254/16, fe80::/10) / 未指定(0.0.0.0, ::) / マルチキャスト | 名前解決した**全アドレス**を検査し、一つでも該当すれば拒否 |
| メタデータ IP(169.254.169.254)/ CGNAT(100.64/10) | 同上 |
| scheme が http/https 以外 | 拒否 |
| ユーザ情報つき URL(`user:pass@host`) | 拒否 |

- 名前解決は**接続時に再検査**する(`DialContext` で dial 先 IP を検査 — DNS リバインディング対策)
- リダイレクト先も再検証し、5 回を超えたら「Too Many Redirects」
- リダイレクト先が §2.3 の同一ドメインでなければ「Redirect off-domain」として中断

### 2.5 上限・既定値(原本 §4 を採用)

| 項目 | 既定 | 最小 | 最大 |
|---|---|---|---|
| workers | 5 | 1 | 20 |
| maxPages | 50 | 1 | 100 |
| requestDelayMs | 200 | 0 | 5000 |
| HTTP タイムアウト | 5 s | 固定 | 固定(原本の「最大 10 秒」は MVP では設定不可) |
| クロール全体の上限時間 | 60 s | 固定 | 固定 |
| 応答本文の読取上限 | 2 MiB | 固定 | 固定 |
| リダイレクト回数 | 5 | 固定 | 固定 |

### 2.6 画面(HTML / CSS / Vanilla JS・ビルド無し)

| ID | 要求 | 優先度 | 原本 |
|---|---|---|---|
| F-30 | 入力欄: Target URL / Workers(スライダ)/ Max Pages(スライダ)/ Request Delay / START / STOP | must | §5 |
| F-31 | Worker 表示: ID・状態(色だけでなく記号と文字で識別)・現在の URL | must | §17 |
| F-32 | 統計表示: Pages / Success / Errors / Elapsed / Requests/sec / Avg / P95 | must | §20 |
| F-33 | ページ一覧: URL(パス表示)/ Status / Time。各行に**状態の記号**(✓ OK / → 転送 / ✕ 不在・到達せず / ! 障害 / · その他)を付ける。行を押すと F-11b の書誌情報が開く | must | §5, §22, §34 D/E |
| F-33b | **リンク切れの一覧**: 取れなかったページを、**それを指しているページ(参照元)**と対で出す。直すのは参照元の側なので、URL だけでは足りない(L9 実装) | could | §34 D |
| F-34 | リンクグラフ: SVG。ノード=ページ、エッジ=リンク。取得済み/未取得を区別 | must | §16 |
| F-35 | 「自分が管理するサイト、またはクロールが許可されているサイトで利用してください」を常時表示 | must | §4 |
| F-36 | SSE は `fetch` + `ReadableStream` で受ける(POST なので `EventSource` は使えない)。**一括到着でも同じ結果になる**(§7 D-02) | must | §18 |
| F-37 | フリート共通フッタ(MIT License © 2026 坂田哲朗 ・ GitHub ・ 歩き方 ・ 設計図 ・ App Menu、下部固定) | must | フリート規約 |
| F-38 | ベンチマーク表: 同じ URL を **Workers=1/2/5/10** で順に走らせ、Time と Pages/s を表と棒で出す(L6 実装)。実行前に確認を取り(対象への総アクセス数を明示)、maxPages はベンチ中 30 件に絞る。**全行が同じページ数を取れたときだけ速度比を出す** — 揃わなければ理由を書いて数を出さない(G-13) | could | §21 |

## 3. 非機能要求

| ID | 要求 | 検証方法 |
|---|---|---|
| N-01 | 依存は Go 標準ライブラリ + `golang.org/x/net/html` のみ。DB・外部 API・課金経路なし | `go.mod` の require を数える(T-0xx) |
| N-02 | テストはネットワークに出ない。`httptest.Server` を対象にする | テスト内で外部ホストを参照しない(検査) |
| N-03 | Vercel Hobby の無料枠で動く。常駐せず、1 リクエスト = 1 クロール ≤ 60 s | §2.5 の上限時間 |
| N-04 | フロントはビルド工程なし(`public/` をそのまま配る) | `vercel.json` / 配信物の目視 |
| N-05 | 日本語本文に別字種・制御文字を混ぜない | `python harness/text_hygiene.py` |

## 4. 品質基準(ゲート)

各 `G-xx` は少なくとも一つのテストから ID で参照される(HC-157)。未実装のものは「未実装」と書く。

| ID | ゲート | 判定 | 状態 |
|---|---|---|---|
| G-01 | URL 正規化は冪等(`normalize(normalize(u)) == normalize(u)`)で、フラグメント除去・ホスト小文字化・既定ポート除去・空パス→`/` を満たす | 表駆動テスト | L1 |
| G-02 | SSRF 拒否表(§2.4)の各行を拒否し、公開アドレス相当は通す。**陽性対照と陰性対照を対で置く** | 表駆動テスト | L1 |
| G-03 | リンク抽出: 相対/絶対/`//host`/`mailto:`/`javascript:`/`#frag`/`<base href>` を仕様どおり扱う | 表駆動テスト | L1 |
| G-04 | `URLSet` は並行 `Add` で同じ URL を二度通さない。`-race` で緑 | 100 goroutine × 同一 URL | L1 |
| G-05 | 取得ページ数 ≤ maxPages。かつ到達可能ページ数 ≥ maxPages のサイトでは = maxPages | httptest の合成サイト | L2 |
| G-06 | **Worker 数と実際の並行度が一致する**: 応答を遅延させたサーバで同時接続数の最大値を測り、workers=N のとき max concurrent = N(N=1,3,5) | httptest でカウント | L2 |
| G-07 | cancel 後に新規リクエストが発生しない(cancel 時刻より後に始まった取得が 0)。全 goroutine が終了する(`wg.Wait` が返る) | httptest + 時刻記録 | L2 |
| G-08 | 統計の独立再計算: サーバの `statistics` と、イベント列から再計算した値(total / success / errors / avg / p95)が一致 | イベントから再計算 | L3 |
| G-09 | SSE の一括到着と逐次到着で、フロントの最終状態(pages / links / stats)が一致 | Node で app.js の reducer を両様で回す | L4 |
| G-10 | 実ブラウザ検品: 合成サイトをクロールして、Worker 行の数 = workers、グラフのノード数 = pages 数、要素が viewBox に収まる(HC-159) | Playwright | L4 |
| G-11 | 本番: `GET /api/health` が 200、`POST /api/crawl` の**最初のイベント到着が完了より先**(=ストリーミングが効いている)を測る。効かなければ D-02 の見込みを実測で上書きする | 本番 URL への実リクエスト(`scripts/probe_stream.mjs`) | 実測 2026-09-07: 320 ms → 2,318 ms(D-02) |
| G-12 | 同一 URL の重複エッジは出さない(`link_found` の (from,to) は一意) | イベント列の集合 | L2 |
| G-13 | ベンチマークの速度比は、**全行が同じページ数を取れたときだけ**出す。揃わなければ数を出さず理由を書く(HC-079: 裏づけの無い数を表に出さない) | `benchSummary().comparable` の単体検査と実ブラウザ | L6 |
| G-15 | 状態の区分(`ok` / `redirect` / `missing` / `server` / `failed` / `other`)は Go 側と画面側で**同じ表**を返す。どちらかを直せばもう一方のテストが落ちる | 同じ 11 行の表を両言語のテストに置く | L9 |
| G-14 | robots.txt の判定は RFC 9309 の規範に一致する。群の選択・最長一致・同長時の Allow 優先・`*`/`$`・percent-encoding・状態ごとの分岐(2xx/4xx/5xx)をそれぞれ検査し、**陽性対照(`Disallow: /` が実際に撃つ)と、除外が 0 件なら落とす検査**を対で置く | 表駆動 + httptest + 実ブラウザ | L7 |

## 5. データモデルとイベント

```go
type Page struct {
    URL        string `json:"url"`
    StatusCode int    `json:"statusCode"`
    DurationMs int64  `json:"durationMs"`
    Title      string `json:"title"`
    Error      string `json:"error,omitempty"`   // §22 の種別: "HTTP 404" / "Timeout" / "DNS Error" / "Non HTML" / "Too Many Redirects" / "Cancelled" / "Redirect off-domain" / "Connection Error"
    WorkerID   int    `json:"workerId"`
}
type Link struct { From, To string }             // 正規化後の URL
type Statistics struct {
    Total, Success, Errors int
    DurationMs             int64
    RequestsPerSec         float64
    AvgMs, P95Ms           int64
}
```

イベント(SSE の `event:` 名 = `type`):

| type | payload |
|---|---|
| `crawl_started` | `{crawlId, url(正規化後), workers, maxPages, requestDelayMs, startedAt, robots, crawlDelayMs}`。`robots` は `obeyed` / `absent` / `unreachable` / `ignored`(F-17) |
| `worker_started` | `{workerId, url, t}`(t = 開始からの ms) |
| `page_completed` | `{workerId, url, statusCode, durationMs, title, error?, description?, h1?, canonical?, finalUrl?, t}` |
| `link_found` | `{from, to, queued, t}`(同一ドメインの**一意な辺**すべて。`queued` が true なら `to` がこのとき URLSet に入りキューへ送られた。既知の URL・上限で入らなかった URL への辺は false。**辺の集合は結果の `links` と一致し**、画面はこれでリンク構造(閉路・上限で切られた先)を描く — L4 で改訂) |
| `worker_done` | `{workerId, pages, t}`(goroutine が抜けた) |
| `crawl_completed` | `{reason: "exhausted" \| "max_pages" \| "cancelled" \| "deadline" \| "robots", statistics, robotsBlocked, t}` |

- **P95** は取得済みページの `durationMs` を昇順に並べ、`ceil(0.95 × n)` 番目(1 始まり)の値
- **Requests/sec** は `total / (durationMs / 1000)`
- `Success` は 2xx かつ HTML。`Errors` は `error` が空でないページの数。`Total = Success + Errors`

## 6. ループ計画

| ループ | 範囲 | 出荷物 |
|---|---|---|
| L0 | 足場・Go 導入・SPEC/TEST_SPEC・Vercel 方式の調査 | `go.mod`・`main.go`(health のみ) |
| L1 | `internal/security`(F-02, §2.4)・`internal/crawler` の parser / queue(F-05, F-06, F-07, G-01..G-04) | 単体テスト |
| L2 | Worker Pool・fetcher・context・統計(F-03, F-04, F-09..F-15, G-05..G-07, G-12) | httptest 統合テスト |
| L3 | HTTP API と SSE(F-20..F-25, G-08) | `go run .` でローカル稼働 |
| L4 | 画面(F-30..F-37, G-09, G-10) | `public/` |
| L5 | GitHub・Vercel・README/ARCHITECTURE/SECURITY・app-menu(G-11) | 本番 URL |
| L6 | ベンチマーク(F-38) | could |

## 7. 設計判断(原本との差)

| ID | 判断 | 理由 |
|---|---|---|
| D-01 | Vercel の **Go Framework Preset**(ルート `main.go`・`PORT` で待ち受け・`framework: "go"`)を採る。原本 §9 の `api/*.go` 方式は取らない | Vercel 公式 docs(2026-08-11 更新)が推奨。旧 `api/*.go` 方式は `http.Flusher` 非対応で SSE が流せない(Vercel Community 2025-03-11 の公式回答) |
| D-02 | 見込みだった「Framework Preset では `Flusher` が効き SSE が逐次届く」は**実測で成立**(2026-09-07・本番 go-parallel-web-crawler.vercel.app)。saijiki-lens を対象に workers=2 / maxPages=8 / delay=300ms で POST: チャンク 16 個、最初のチャンク 320 ms、最後 2,318 ms、イベント 116 件。go.dev でもチャンク 19 個(296 → 2,281 ms)。**最初のイベントは完了より約 2 秒先に届く**。効かない場合の保険(一括到着でも同じ最終状態 F-36 / G-09)は残す | 起票時は未実測(Node/Python 以外のストリーミングは docs に明記がない)。L5 で本番に対して測った |
| D-03 | 原本 §19 の `crawlId` 発行 → 別エンドポイントで events 取得、の二段は取らず、**`POST /api/crawl` が直接 SSE を返す** | サーバレスでは呼び出しごとにインスタンスが違いうるので、メモリ上の crawl 台帳を別リクエストから引けない。一本のストリームなら保証がいらない |
| D-04 | リンク抽出は正規表現でなく `golang.org/x/net/html` のトークナイザ | 属性の引用符・大文字小文字・`<base>` を正規表現で正しく扱うのは難しい。準標準ライブラリ 1 つだけ足す |
| D-05 | Request Delay は Worker ごとの取得前スリープ | 「N Workers で並列に取得している」ことを見せたいので、全体のレートリミットではなく Worker 単位にする |
| D-06 | `jobs` channel の容量 = maxPages。`URLSet` が maxPages 件で受付を止める | これで manager が `jobs` への送信でブロックしない(送受信の相互待ちによるデッドロックを構造で排除する)。教材として説明しやすい |
| D-07 | UI の文言は日本語、固有名詞(アプリ名・キャッチコピー・Worker/Queue 等の Go 用語)は英語のまま | フリートの他アプリと揃える。原本の画面例は英語だが用途は日本語話者の学習 |

## 8. スコープ外

原本 §3 のとおり。ログイン・DB・履歴・JS レンダリング・サイトマップの巡回・
認証ページ・外部ドメインの巡回・長時間クロール。

**robots.txt は L7 で実装した(F-16〜F-18)**。ただし RFC 9309 の全部ではない —— キャッシュ
(1 クロール 1 回しか読まないので不要)と、robots.txt 自体のリダイレクト追跡の独自制御
(`http.Client` 任せ。同一ドメイン制限がそのまま効く)は実装していない。
`Sitemap:` 行は収集するが辿らない。
