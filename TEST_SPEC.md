# TEST_SPEC.md — go-parallel-web-crawler

<!-- scaffold template v1.30.0 から展開(2026-09-06) -->

## 実行規約

- `go test -race ./...` を stage 3–5 の判定に使用する(`-race` は G-04 / G-07 の要件)
- テストは**ネットワークに出ない**(N-02)。取得対象はすべて `net/http/httptest` のサーバ
- フロント(`public/app.js`)の reducer は Node の `node --test` で検査する(G-09)。ブラウザ検品は Playwright(G-10)
- フィクスチャ更新は専用コミット(`test: update fixtures`)で行い、理由をループログに記す
- 解析解を期待する合成フィクスチャは、期待値の導出前提を**テスト内の assert で検算**し、
  導出過程をコメントに残す。前提を検算しない期待値は正しい実装を落とす(VERIF-FALSE / HC-004)

## 期待値の出所(HC-016)

| 出所 | 書き方 |
|---|---|
| SPEC の条項 | 条項 ID を書く。**SPEC の保証粒度を超える期待値を書かない** |
| 外部権威(RFC 3986 / RFC 1918 / Go 標準ライブラリの挙動) | 出典をテストのコメントに書く |
| 実測 | **実測日と実測値**をコメントに残す |

件数は定数で書かず、**集合の一致・取りこぼしの不在**という不変量で書く。
ただし合成サイト(httptest)は自分で作るので構造が既知であり、「到達可能ページ数」は
サイト生成関数から**導出**して使う(定数を二重に書かない — HC-068)。

## オラクルの出所

| フィクスチャ | 出所 | 性格 |
|---|---|---|
| `internal/security` の拒否表 | SPEC §2.4(RFC 1918 / RFC 4193 / RFC 3927 / RFC 6598) | 外部権威 |
| 合成サイト `testsite`(httptest) | 生成関数がページ集合と辺集合を返す | 自作・構造既知 |
| 統計の再計算 | イベント列から Python 側と独立に Go テスト内で再計算 | 二経路一致(G-08) |
| フロント reducer | 同じイベント列を「一括」「一件ずつ」で流す | 二経路一致(G-09) |

## ケース一覧

**「対応要求」には SPEC の ID を必ず書く**(HC-157)。

### L0

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-001 | F-20 | `GET /api/health` | 200・`{"status":"ok"}`・`Content-Type: application/json` |
| T-002 | N-01 | `go.mod` の require | `golang.org/x/net` 以外の直接依存が無い |

### L1(security / parser / queue)

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-101 | F-02, G-02 | SSRF 拒否表(§2.4)の各行(ホスト名・IPv4・IPv6・scheme・userinfo) | すべて拒否。理由文字列が行ごとに異なる |
| T-102 | F-02, G-02 | 陰性対照: 公開アドレス相当(例 93.184.216.34、2606:2800::1、`example.com` を固定解決) | 通す |
| T-103 | F-02, G-02 | 陽性対照: 名前解決が公開 IP と私有 IP を**混ぜて**返す | 拒否(一つでも該当すれば拒否) |
| T-104 | G-01 | 正規化の表: フラグメント・ホスト大文字・`:443`・空パス・`..` の解決 | 表の期待値。冪等性 |
| T-105 | F-06 | 同一ドメインの表: 完全一致 / `www.` 差 / サブドメイン / 別ドメイン / ポート差 | §2.3 の判定 |
| T-106 | F-07, G-03 | リンク抽出の表: 相対 / 絶対 / `//host` / `mailto:` / `javascript:` / `#` のみ / `<base href>` / 属性なし `<a>` / 大文字 `HREF` | 抽出集合が期待集合と一致 |
| T-107 | F-05, G-04 | `URLSet.Add` を 100 goroutine から同一 URL で同時に呼ぶ | true が正確に 1 回 |
| T-108 | F-05, D-06 | `URLSet` は上限 N 件で `Add` を拒む | N+1 件目が false・`Len()==N` |

### L2(worker pool / fetcher / crawler)

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-201 | F-10, G-05 | 到達可能 30 ページの合成サイトを maxPages=10 でクロール | 取得 = 10 |
| T-202 | F-03, G-05 | 同サイトを maxPages=100 でクロール | 取得 = 到達可能数(生成関数から導出)。到達不能ページは取らない |
| T-203 | F-04, G-06 | 応答を 150 ms 遅延させるサーバ・workers=1,3,5 | 同時接続数の最大 = workers |
| T-204 | F-09, G-07 | 開始 200 ms 後に cancel | cancel 後に始まった取得 0・`Run` が返る・`crawl_completed.reason == "cancelled"` |
| T-205 | F-08 | `image/png` を返す URL | `error == "Non HTML"`・リンク抽出しない |
| T-206 | F-11 | 404 / 500 / タイムアウト(遅延 > 5 s は長いのでタイムアウトを注入可能にする)/ リダイレクト 6 回 | 種別が §5 の文字列 |
| T-207 | F-06, G-12 | 同じリンクを二か所に持つページ | `link_found` の (from,to) が一意。外部ドメインは出ない |
| T-208 | F-13 | イベント列の Worker 状態遷移 | 各 Worker について started/completed が交互・最後に `worker_done` |
| T-209 | F-14 | requestDelayMs=100・workers=1・5 ページ | 総時間 ≥ 5 × 100 ms |
| T-210 | F-15 | 上限時間を 300 ms に注入・遅いサーバ | `reason == "deadline"` |

### L3(API)

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-301 | F-21, F-25 | `POST /api/crawl` を合成サイトへ | `text/event-stream`・先頭 `crawl_started`・末尾 `crawl_completed` |
| T-302 | F-22 | workers=0 / 21・maxPages=0 / 101・delay=-1 / 5001・不正 URL・内部 URL | 400・`error` に理由 |
| T-303 | G-08 | T-301 のイベント列から統計を再計算 | `crawl_completed.statistics` と一致 |
| T-304 | F-23 | `/stop` を呼ぶ | 進行中クロールが `cancelled` で終わる |
| T-305 | F-09 | クライアントが接続を切る | サーバの `Run` が返る(goroutine が残らない) |

### L4(UI)

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-401 | G-09 | reducer に同じイベント列を一括 / 一件ずつ | 最終状態が deepEqual |
| T-402 | G-10 | Playwright: 合成サイトをクロール | Worker 行数 = workers・ノード数 = pages・全要素が viewBox 内・横溢れ無し |
| T-403 | F-37 | フッタ 5 項目 | 規約の並び(DOM で検査) |
| T-404 | N-05 | `python harness/text_hygiene.py` | 違反 0 |

### L5(本番)

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-501 | G-11 | 本番 `GET /api/health` | 200 |
| T-502 | G-11, D-02 | 本番 `POST /api/crawl`(自サイト)で最初のイベントの到着時刻 | 完了より先に届く(届かなければ D-02 を実測で上書き) |
