# ARCHITECTURE.md — Go Parallel Web Crawler

実装前にここを読む(原本 §32)。仕様の正本は [SPEC.md](SPEC.md)、テストの対応表は [TEST_SPEC.md](TEST_SPEC.md)。

## 全体

```
Browser (public/)                      Go server (main.go, PORT)
┌───────────────────────┐              ┌──────────────────────────────────────────┐
│ app.js                │  POST JSON   │ internal/api                              │
│  fetch + ReadableStream├─────────────▶│  Crawl: validate(§2.5, §2.4) → SSE        │
│  AbortController      │◀─────────────┤  Stop / Result: registry(メモリ・上限つき)│
│ state.js (reducer)    │  text/event- │            │                              │
│  SSE parser           │  stream      │            ▼                              │
│  workers / nodes /    │              │ internal/crawler.Run(ctx, cfg, sink)      │
│  links / stats        │              │            │                              │
└───────────────────────┘              │   jobs ──▶ worker × N ──▶ results         │
                                       │   (chan)   (goroutine)    (chan)          │
                                       │            │                              │
                                       │   events (chan) ──▶ deliver goroutine ──▶ sink
                                       │                                           │
                                       │ internal/security: ValidateURL / DialControl
                                       └──────────────────────────────────────────┘
```

- **1 リクエスト = 1 クロール。** `POST /api/crawl` が検証を通すとその場で SSE を流し始め、
  `crawl_completed` を送って閉じる(SPEC D-03)。サーバレスで台帳を別リクエストから引けない問題を
  「引かなくてよい設計」で避けている
- **STOP は二経路。** `/stop`(同じインスタンスに当たれば台帳の cancel が効く)と fetch の abort
  (`r.Context()` が cancel される)。どちらも `crawler.Run` に渡した同じ `ctx` を止める
- **画面はビルド無し。** `public/` をそのまま配る。`state.js` は Node のテストからも import する

## クローラ本体(internal/crawler)

| ファイル | 役割 | Go の題材 |
|---|---|---|
| `crawler.go` | `Run`: manager。BFS の勘定(inflight)、URLSet、辺の一意化、完了理由、統計 | channel の閉じ方・WaitGroup |
| `worker.go` | `worker`: jobs を range し、delay → Fetch → events / results | goroutine・`select` と `ctx.Done()` |
| `queue.go` | `URLSet`: `sync.Mutex` + map。上限 N 件で受付を止める(D-06) | Mutex |
| `fetcher.go` | `NewClient`: dial 時の IP 再検査・リダイレクト 5 回・別ドメイン拒否。`Fetch`: 2xx かつ HTML だけ解析 | `net/http`・`http.Client.CheckRedirect` |
| `parser.go` | `ExtractLinks`: `x/net/html` のトークナイザで `<a href>` と `<base>` と `<title>` | URL の解決 |
| `urlnorm.go` | `Normalize`(冪等)・`SameDomain`(www. 差のみ同一) | `net/url` |

### なぜデッドロックしないか(D-06)

manager は `results` を受け取りながら `jobs` に送る。Worker は `jobs` を受け取りながら `results` に送る。
両方が満杯になると互いに待って止まる —— これが典型的な相互待ちである。

本実装は `jobs` の容量を `maxPages` にし、`URLSet` が `maxPages` 件で受付を止める。
**送られる job の総数が容量を超えない**ので、manager の `jobs <- url` は決してブロックしない。
`results` は容量 0 だが、manager は常に受信側にいる。`events` は容量 64 で、専用の配送 goroutine が
読み続ける。三本の channel のどれにも「送り手が受け手を待ったまま受け手が送り手を待つ」経路が無い。

### なぜ止まるか(G-07)

`ctx` が cancel されると:
1. 取得中の `http.Client.Do` はすぐ error を返す(`Cancelled`)
2. Worker はループ先頭で `ctx.Err()` を見て、残りの job を**取得せず** `skipped` で返す
3. manager は `skipped` も inflight に数えるので勘定が合い、`inflight == 0` で `close(jobs)`
4. Worker は range を抜けて `worker_done` を送り、`wg.Wait()` が返る

cancel 後に始まる HTTP リクエストは 0 件(T-204 で時刻を突き合わせて実測)。

## API(internal/api)

| 経路 | 役割 |
|---|---|
| `GET /api/health` | 固定応答 |
| `POST /api/crawl` | 境界(§2.5)→ URL 検証(§2.4)→ `crawler.Run` → SSE。`http.Flusher` があれば 1 件ごとに Flush |
| `POST /api/crawl/{id}/stop` | 台帳の cancel |
| `GET /api/crawl/{id}` | 台帳の結果(完了済み 50 件・10 分) |

SSE の 1 件は `event: <type>` + `data: <JSON>`。`type` の一覧と payload は SPEC §5。

## 画面(public/)

- `state.js`: `createSSEParser`(チャンク境界を跨ぐ)/ `reduce`(イベント → 状態)/ `liveStats` / `channelCounts`
- `app.js`: fetch と描画。Worker 行は `data-state` 属性で色と記号を切り替える。グラフは自前の力学レイアウト
  (反発・ばね・中心へ)を `requestAnimationFrame` で回し、ノードは viewBox の内側に clamp する
- 「Channel の中身」は `discovered − done − crawling` をキュー待ちとして出す。
  `discovered` は `queued=true` の `link_found` と開始 URL の数(= URLSet の大きさ)

## テスト

| 層 | 道具 | 対象 |
|---|---|---|
| 単体・統合(Go) | `go test -race ./...`・`httptest` の合成サイト | 正規化・SSRF・抽出・URLSet・Worker Pool・API |
| reducer(Node) | `node --test tests/ui/reducer.test.mjs`・実録 SSE フィクスチャ | 一括 / 1 文字ずつ / 行ごとの一致(G-09) |
| 実ブラウザ(Playwright) | `node --test tests/ui/browser.test.mjs` | Worker 行・ノード・幾何(viewBox 内包)・溢れ・STOP・フッタ |
| 本番 | `node scripts/probe_stream.mjs` | SSE が逐次届くか(G-11) |

フィクスチャの再録: `GPWC_RECORD_FIXTURES=1 go test ./internal/api -run TestRecordFixtures`。

## Vercel

Go Framework Preset(`vercel.json` の `"framework": "go"`)。ルートの `main.go` が `PORT` で待ち受け、
`public/` を `http.FileServer` で配る。ストリーミングは本番で実測済み(SPEC D-02)。
`.vercelignore` でログ・文書・検品の出力を除く。
