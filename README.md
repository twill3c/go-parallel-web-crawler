# Go Parallel Web Crawler

**See Go concurrency in action.**

URL を一つ渡すと、そのサイト内のページを幅優先で巡回し、**Go の goroutine / channel /
Worker Pool / context / sync.Mutex が実際のクロールでどう動いているか**をブラウザで見せる
学習用アプリ。クロール結果よりも「Worker が今なにをしているか」を最重要の画面とする。

構想の原本は [docs/go-parallel-web-crawler-spec.md](docs/go-parallel-web-crawler-spec.md)、
要求 ID 付きの正本は [SPEC.md](SPEC.md)、テストの対応表は [TEST_SPEC.md](TEST_SPEC.md)。

## 状態

**L0(足場)まで完了。** `GET /api/health` だけが動く。クローラ本体は L1 以降。

| ループ | 範囲 | 状態 |
|---|---|---|
| L0 | 足場・Go 導入・SPEC/TEST_SPEC・Vercel 方式の調査 | 完了(2026-09-06) |
| L1 | URL 検証(SSRF)・正規化・リンク抽出・URLSet | 未着手 |
| L2 | Worker Pool・fetcher・context・統計 | 未着手 |
| L3 | HTTP API と SSE | 未着手 |
| L4 | 画面(Worker 表示・統計・SVG グラフ) | 未着手 |
| L5 | GitHub・Vercel・文書 | 未着手 |

## What can you learn?

- goroutine
- channel
- Worker Pool
- sync.Mutex
- context cancellation
- HTTP client(net/http)
- URL parsing
- concurrent processing
- performance measurement

## 動かす

```bash
go run .                 # http://localhost:3000
go test -race ./...      # テスト(ネットワークに出ない)
```

Windows で `-race` を使うには 64 bit の C コンパイラ(mingw-w64)が要る。

## 利用条件

学習・デモ用途の小規模クローラです。**自分が管理するサイト、またはクロールが許可されている
サイトで利用してください。** Workers ≤ 20・Max Pages ≤ 100・1 クロール ≤ 60 秒に固定しています。

## 構成

```
main.go              PORT で待ち受ける net/http サーバ(Vercel Go Framework Preset)
internal/api         HTTP ハンドラ(health / crawl / SSE)
internal/crawler     クローラ本体(worker pool / queue / fetcher / parser)
internal/security    URL 検証(SSRF 対策)
internal/model       データモデル
public/              画面(HTML / CSS / Vanilla JS、ビルド無し)
tests/               リポジトリ横断の検査
```

## License

MIT © 2026 坂田哲朗
