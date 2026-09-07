// bench/site.mjs — 比較に使う合成サイト。Go 側と TS 側の**両方が同じものを相手にする**。
//
// 調整できるもの:
//   pages   ページ数(/ と /p1..pN)
//   delay   1 応答あたりの遅延 ms(ネットワーク待ちの再現)
//   bytes   1 ページの本文の目安バイト数(解析の負荷)
//   links   1 ページから張るリンクの本数
import { createServer } from 'node:http';

/** startSite は合成サイトを立ち上げ、{ url, close, hits } を返す。 */
export function startSite({ pages = 30, delay = 0, bytes = 4000, links = 8 } = {}) {
  let hits = 0;
  const filler = '<p>' + 'あ'.repeat(Math.max(0, Math.floor(bytes / 3))) + '</p>';

  // **本文は起動時に組んで Buffer にしておく。** 応答ごとに文字列を組んで UTF-8 に符号化すると、
  // 1 スレッドの Node サーバが律速になり、クローラでなくサイトを測ることになる
  // (2026-09-07 実測: 本文 60KB のとき Go の Workers 5 が 1 に対して 2.1 倍しか速くならなかった)。
  const bodies = new Map();
  for (let i = 0; i <= pages; i++) {
    let h = `<!doctype html><html><head><title>Page ${i}</title></head><body>`;
    // 各ページから links 本のリンクを張る。番号を散らして木でなく網にする
    for (let k = 1; k <= links; k++) {
      const t = ((i + k * 7) % pages) + 1;
      h += `<a href="/p${t}">to ${t}</a>`;
    }
    h += `<a href="/">home</a><a href="https://other.example.org/x">ext</a><a href="/asset.png">img</a>`;
    h += filler + '</body></html>';
    bodies.set(i, Buffer.from(h, 'utf8'));
  }

  const server = createServer((req, res) => {
    hits++;
    const send = () => {
      const m = /^\/p(\d+)/.exec(req.url ?? '');
      const i = m ? Number(m[1]) : 0;
      const body = bodies.get(i) ?? bodies.get(0);
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Content-Length': body.length });
      res.end(body);
    };
    if (delay > 0) setTimeout(send, delay); else send();
  });
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      resolve({
        url: `http://127.0.0.1:${port}/`,
        close: () => new Promise((r) => server.close(r)),
        hits: () => hits,
      });
    });
  });
}
