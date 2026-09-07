# TEST_SPEC.md — go-parallel-web-crawler

<!-- scaffold template v1.30.0 から展開(2026-09-06) -->

## 実行規約

- `go test -race ./...` を stage 3–5 の判定に使用する(`-race` は G-04 / G-07 の要件)
- テストは**ネットワークに出ない**(N-02)。取得対象はすべて `net/http/httptest` のサーバ
- フロント(`public/app.js`)の reducer は Node の `node --test` で検査する(G-09)。ブラウザ検品は Playwright(G-10)。
  **`node --test tests/ui/*.test.mjs` とファイルを指定する。** ディレクトリを渡すと Node 24.3 では
  `MODULE_NOT_FOUND` で落ちる —— 走らせ方が壊れていると緑も赤も信用できない(2026-09-07 実測)
- フィクスチャ更新は専用コミット(`test: update fixtures`)で行い、理由をループログに記す
- **DOM や表の中身を検査するケースは、期待値を書く前に一度走らせて実際の文字列を出力させる**(HC-195)。
  自作の合成サイト・自作のイベント契約でも同じ。「自分で決めたのだから知っているはず」で書くと、
  正しい実装が落ちる(実際に 2 度落とした)。**整形した文字列ではなく、そこから読み取れる値で比べる**
  (件数なら `parseInt`。`'12'` と `'12 (err 1)'` は同じ事実の別表記)
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

### L6(ベンチマーク)

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-601 | F-38 | `benchRow` / `benchSummary` の集計(速度比・棒・comparable・0/1 行・durationMs=0) | 式から導出した値。ページ数が揃わなければ `comparable=false` |
| T-602 | F-38, G-10 | Playwright: BENCHMARK を押して 4 行そろう | Workers 1/2/5/10・全行が同じページ数・最速行の印が 1 行・棒が 0〜100%・速度比が出る・横溢れ無し |
| T-602b | F-38 | 確認ダイアログをキャンセル | クロールが始まらない(状態は idle・表は隠れたまま) |

### L7(robots.txt)

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-701 | F-16, G-14 | 群の選択(自分の名前・大文字小文字違い・複数一致の結合・`*` への後退・一致なし) | RFC 9309 §2.2.1 のとおり |
| T-702 | F-16, G-14 | 最長一致と同長時の Allow 優先 | RFC 9309 §2.2.2。オクテット数の多い規則が勝つ |
| T-703 | F-16, G-14 | `*`(0 個以上)と `$`(終端) | `/a.pdf` は拒否、`/a.pdf.html` は許可 |
| T-704 | F-16 | 構文(コメント・空白・未知フィールド・値なし Disallow・空行での群の切れ目・Crawl-delay・Sitemap) | 解析可能な規則だけを使う |
| T-705 | F-16, G-14 | percent-encoding(`/%E6%97%A5%E6%9C%AC/` と `/日本/`) | 両側を復号して同じ資源と見なす |
| T-706 | F-16 | 空・コメントのみ・規則なしの群 | すべて許可 |
| T-707 | G-14 | **陽性対照**: `Disallow: /` | すべて拒否(撃たなければ検査が働いていない) |
| T-711 | F-16, G-14 | 200 の robots.txt で Disallow のパスを辿らない | 除外件数 > 0(0 なら Fatal)・開いているページは取れる |
| T-712 | F-17 | 404 | すべて許可・`robots: absent` |
| T-713 | F-17 | 5xx | **0 ページ**・`reason: robots`・`robots: unreachable`・完了イベントは出る |
| T-714 | F-17 | Crawl-delay 0.2 秒 / 利用者 0 | 実所要 ≥ 600ms(3 ページ)・`effectiveDelayMs = 200`。陰性対照: 利用者 300ms のほうが大きければ 300 |
| T-715 | F-18 | `RespectRobots=false` | robots.txt を取りに行かない(取得回数 0) |
| T-721 | F-16, F-18 | API 既定(`ignoreRobots` を送らない) | 従う。取得 1 回・`robotsBlocked > 0` |
| T-722 | F-18 | `ignoreRobots: true` | 取りに行かず、`Disallow: /` でも取得する |
| T-723 | F-17 | `Disallow: /` を既定で | 0 ページ・`reason: robots` |
| T-724 | F-17 | API 経由の Crawl-delay | `crawlDelayMs = 150`・`requestDelayMs = 150`・実所要 ≥ 450ms |
| T-725 | F-17 | API 経由の 5xx | `robots: unreachable`・`reason: robots` |
| T-726 | F-16, G-14 | 実ブラウザ: 状態と除外件数の表示 | 「取得して従っています」・除外 > 0・`/p1` と `/p11` は無く `/p10` はある(最長一致) |
| T-727 | F-18 | 実ブラウザ: チェックを外す | 拒否されたページも取る・「従わない設定」と出る |
| T-728 | F-17 | 実ブラウザ: `Disallow: /` | 0 ページ・理由に「robots.txt がこのクロールを許していない」 |

### L9(リンク切れと書誌情報)

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-901 | F-11b | `ExtractPage` が title / description / h1 / canonical を拾う | 空白は畳む・最初の h1 だけ・`og:description` は使わない・canonical は絶対化して正規化 |
| T-902 | F-11b | 何も無い文書・入れ子の h1 | すべて空 / `前中後` |
| T-903 | F-11b | canonical と自 URL の異同(5 通り) | 正規化して比べれば「自分自身か」が言える |
| T-904 | F-33, G-15 | `StatusClass` の 11 行の表 | ok / redirect / missing / server / failed / other |
| T-905 | F-33b | 参照元の逆引き | 壊れた URL を指すページを漏れなく・誰も指していない URL には付かない |
| T-906 | F-33, F-33b, G-15 | 画面側の `statusClass` / `STATUS_SYMBOL` / `referrers` / `brokenPages` | **T-904 と同じ表**を使う(片方だけ直すと落ちる)・記号と読みが全区分にある |
| T-907 | F-11b, G-10 | 実ブラウザ: 詳細行 | 初期状態で閉じている(computed display が none)・押すと 1 行だけ開き h1 / description / canonical が出る |

### L5(本番)

| ID | 対応要求 | ケース | 期待 |
|---|---|---|---|
| T-501 | G-11 | 本番 `GET /api/health` | 200 |
| T-502 | G-11, D-02 | 本番 `POST /api/crawl`(自サイト)で最初のイベントの到着時刻 | 完了より先に届く(届かなければ D-02 を実測で上書き) |
