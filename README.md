# Go Parallel Web Crawler

**See Go concurrency in action.**

本番: **https://go-parallel-web-crawler.vercel.app**

URL を一つ渡すと、そのサイト内のページを幅優先で巡回し、**Go の goroutine / channel /
Worker Pool / context / sync.Mutex が実際のクロールでどう動いているか**をブラウザで見せる
学習用アプリ。クロール結果よりも「Worker が今なにをしているか」「channel に何件が溜まっているか」
「STOP で context が全 Worker に届くか」を最重要の画面にしている。

構想の原本は [docs/go-parallel-web-crawler-spec.md](docs/go-parallel-web-crawler-spec.md)。
AI コーディングエージェント向けの参照資料は次の四つ(原本 §32):

| 文書 | 役割 |
|---|---|
| [SPEC.md](SPEC.md) | 仕様の正本。要求 F-xx・ゲート G-xx・設計判断 D-xx・実測値 |
| [ARCHITECTURE.md](ARCHITECTURE.md) | 実装前に読む。channel の組み方、なぜデッドロックしないか、なぜ止まるか |
| [TEST_SPEC.md](TEST_SPEC.md) | テストの対応表(原本の TESTCASE.md に相当)。各ケースの期待値の出所 |
| [ROADMAP.md](ROADMAP.md) | 未完了タスク。ここから作業する |

あわせて [SECURITY.md](SECURITY.md)(SSRF 対策で守っていること・いないこと)。

## What can you learn?

| 題材 | どこで見えるか |
|---|---|
| goroutine | Workers 欄の 1 行が 1 goroutine。`worker.go` |
| channel | 「Channel の中身」の jobs / results。`crawler.go` の三本の channel |
| Worker Pool | Workers スライダで本数を変え、Requests/sec の変化を見る |
| sync.Mutex | URLSet(既知の URL)。`queue.go` |
| context cancellation | STOP を押すと全 Worker が ✓ Completed で畳まれる。`crawler.go` / `worker.go` |
| HTTP client(net/http) | `fetcher.go`: タイムアウト・リダイレクト・Content-Type |
| URL parsing | `urlnorm.go` / `parser.go`: 正規化(冪等)・相対参照の解決 |
| concurrent processing | 応答を遅らせた合成サイトで、同時接続数の最大 = Workers を実測(T-203) |
| performance measurement | Elapsed / Requests/sec / Avg / P95。イベント列から再計算して一致(G-08) |
| Worker Pool の効き方 | BENCHMARK ボタン。Workers 1/2/5/10 を順に走らせて Pages/s を並べる。**速すぎる計測では数を出さない**(下限 500ms)|
| HTML の走り読み | 同じトークナイザ走査で description / h1 / canonical を拾う。行を押すと開く |
| リンク切れの直し方 | 取れなかったページに**参照元**を添える。直すのは参照元の側 |

## 動かす

```bash
go run .                                 # http://localhost:3000
go test -race ./...                      # Go のテスト(ネットワークに出ない)
node --test bench/equivalence.test.mjs   # Go 版と TS 版が同じ結果を返すことの検査(比較の前提)
node bench/run.mjs --reps 5              # Go vs TypeScript を測り直す → public/bench-results.json
node --test tests/ui/*.test.mjs          # 画面(reducer・ベンチ集計・実ブラウザ検品)
                                         # ※ ディレクトリ指定は Node 24.3 で落ちる。ファイルを渡す
                                         # ※ Playwright は PLAYWRIGHT_DIR(既定 ../hacchu-forge/node_modules/playwright)から借りる
node scripts/probe_stream.mjs https://go-parallel-web-crawler.vercel.app https://<対象>/  # 本番の SSE を測る
```

Windows で `-race` を使うには 64 bit の C コンパイラ(mingw-w64)が要る。
`.wt/gate.json` の `test_command` は `go test -race ./...`。

## API

| 経路 | 内容 |
|---|---|
| `POST /api/crawl` | `{url, workers(1..20), maxPages(1..100), requestDelayMs(0..5000), ignoreRobots?}` → `text/event-stream`。`crawl_started` / `worker_started` / `page_completed` / `link_found` / `worker_done` / `crawl_completed`。**`ignoreRobots` を省くと robots.txt に従います** |
| `POST /api/crawl/{id}/stop` | 進行中クロールの context を cancel(同じインスタンスに当たったときだけ効く。画面は fetch の abort も併用) |
| `GET /api/crawl/{id}` | 完了済み結果(メモリ・50 件・10 分) |
| `GET /api/health` | `{"status":"ok"}` |

## 利用条件

学習・デモ用途の小規模クローラです。**自分が管理するサイト、またはクロールが許可されている
サイトで利用してください。** Workers ≤ 20・Max Pages ≤ 100・1 クロール ≤ 60 秒・同一ドメインのみ。

**robots.txt には既定で従います**(RFC 9309)。最長一致・`*`/`$`・`Crawl-delay` を実装し、
5xx で取得できないときは 1 ページも取りません。チェックを外せば無視できますが、
それは自分が管理するサイトを試すための逃げ道です。詳細は [SECURITY.md](SECURITY.md)。

## 構成

```
main.go              PORT で待ち受ける net/http サーバ(Vercel Go Framework Preset)
internal/api         HTTP ハンドラ(health / crawl / SSE / stop / result・台帳)
internal/crawler     クローラ本体(manager / worker / queue / fetcher / parser / urlnorm)
internal/security    URL 検証(SSRF 対策・dial 時の再検査)
internal/model       データモデルとイベント
public/              画面(HTML / CSS / Vanilla JS、ビルド無し。state.js は Node のテストと共有)
tests/               リポジトリ横断の検査(go.mod の依存・reducer・実ブラウザ)
scripts/             本番の SSE 計測
bench/               Go vs TypeScript の測定(TS 版クローラ・合成サイト・実行器)。**出荷物ではない**
cmd/                 測定用の CLI(crawlbench / urlbench)。本番サーバは root の main.go
```

## 実測で分かったこと

- **Vercel の Go Framework Preset で SSE は逐次届く**(2026-09-07)。旧来の `api/*.go` 方式は
  `http.Flusher` 非対応で流せない(Vercel Community 2025-03 の公式回答)。本番で 8 ページのクロールを
  測ると、最初のチャンクは 320 ms、最後は 2,318 ms、間にチャンク 16 個(SPEC D-02)
- **統計の独立再計算が最初の実行で食い違いを捕まえた。** Requests/sec を生の経過時間から出していたが、
  イベント列には ms に丸めた `durationMs` しか無く、再計算は原理的に一致しなかった(HC-189)
- **`hidden` 属性は `display` を持つ CSS に負ける。** 要素数・幾何・溢れの検査は全部緑のまま、
  スクリーンショットの目視でだけ見つかった(HC-193)
- **待ち時間があるなら、言語の差はそこに埋もれる**(2026-09-07・ROADMAP G)。同じアルゴリズムの
  TypeScript 版を書いて同じ合成サイトを巡回させると、応答 50ms のとき Go と TypeScript の差は
  Workers 5 で 2%、Workers 1 で 5% だった。一方、待ち時間を含まない計算だけ(URL 正規化 20 万回)を
  切り出すと 3.06 倍の差が出る。**遅延ゼロの条件は測れなかった** —— 合成サイトが 1 スレッドの Node で、
  そちらが律速になったため(Go の Workers 5 が 1 に対して 2.7 倍しか出ない)。測れなかったことも表に残してある
- **ベンチマークは速すぎると何も測れない。** ローカルの合成サイト(1 回 20〜50 ms)では
  Workers 1 が 444 pages/s、5 が 750、10 が 267 と単調ですらない値が出た。差は並行度ではなく
  往復のばらつきである。各行 500 ms 未満なら速度比も最速の印も出さず、手当てを書く(G-13)。
  応答を 300 ms 遅らせた同じサイトでは 3.2 → 12.4 pages/s(×3.9)と単調に伸びる

## License

MIT © 2026 坂田哲朗
