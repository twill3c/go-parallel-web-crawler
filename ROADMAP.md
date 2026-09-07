# ROADMAP.md — Go Parallel Web Crawler

未完了のタスクから作業する(原本 §32)。完了したものは日付を残す。

## 完了

| ループ | 内容 | 完了日 |
|---|---|---|
| L0 | 足場・Go 導入・SPEC/TEST_SPEC・Vercel 方式の調査 | 2026-09-06 |
| L1 | URL 検証(SSRF)・正規化・同一ドメイン・リンク抽出・URLSet | 2026-09-06 |
| L2 | Worker Pool・BFS manager・fetcher・context キャンセル・統計 | 2026-09-06 |
| L3 | HTTP API と SSE・停止・台帳 | 2026-09-06 |
| L4 | 画面(Worker・channel の中身・SVG グラフ・統計・一覧・フッタ)・実ブラウザ検品 | 2026-09-06 |
| L5 | GitHub・Vercel 本番・ストリーミング実測・文書・app-menu | 2026-09-07 |
| L6 | Worker Benchmark(F-38)。速度比を出す前提を二つ置いた(同ページ数・各行 500ms 以上) | 2026-09-07 |
| L7 | robots.txt(F-16〜F-18・RFC 9309)。既定で従う。5xx は全面拒否 | 2026-09-07 |
| L9 | リンク切れ(D: 状態の記号と参照元)と書誌情報(E: description / h1 / canonical) | 2026-09-07 |

原本 §33 の Definition of Done は、上の L0〜L5 ですべて満たした
(clone / build / deploy / URL 入力 / 開始 / 同一ドメイン / 上限 / Worker 数 / goroutine / channel /
重複排除 / Stop / 一覧 / グラフ / Worker 状態 / 処理時間 / エラー / 単体テスト / README / SSRF)。

## 未完了

| ID | 内容 | 優先度 | 備考 |
|---|---|---|---|
| G | Go vs TypeScript 比較(原本 §34 G) | could | 結果を言語性能の一般論にしない、と原本自身が注意している |

## やらないこと

原本 §3 のとおり(ログイン・DB・履歴・JS レンダリング・サイトマップ・認証ページ・外部ドメインの巡回・長時間クロール)。
