// bench/urlbench.ts — URL 正規化と同一ドメイン判定だけを回す(ROADMAP G の測定 2)。
//
// クロール全体はネットワーク待ちが支配するので、言語の差はそこに埋もれる。
// **両方とも自分が書いた同じアルゴリズム**である正規化を切り出せば、
// ランタイムの差だけを見られる。Go 側は cmd/urlbench が同じことをする。
//
// それでもこれは「URL 正規化の速さ」であって、言語の一般的な性能ではない。
import { performance } from 'node:perf_hooks';
import { normalize, sameDomain } from './crawler.ts';

const n = Number(process.argv[2] ?? 100000);

// 入力は決定的に作る(乱数を使うと実行ごとに違うものを測ることになる)
const inputs: string[] = [];
const hosts = ['example.com', 'www.example.com', 'Example.COM', 'sub.example.com', 'example.org'];
const paths = ['/', '/a', '/a/b/../c', '/a?x=1#frag', '/%E6%97%A5%E6%9C%AC/x', '/deep/path/to/page.html'];
for (let i = 0; i < n; i++) {
  const scheme = i % 3 === 0 ? 'http' : 'https';
  const port = i % 7 === 0 ? (scheme === 'http' ? ':80' : ':443') : '';
  inputs.push(`${scheme}://${hosts[i % hosts.length]}${port}${paths[i % paths.length]}`);
}
const start = 'https://example.com/';

// 計測。JIT の暖機のために 1 割を捨ててから測る(Go 側には暖機の概念が無いが、
// **不利な側に合わせない** ——「暖まった V8」と「Go」を比べるほうが V8 に公平)
let sink = 0;
const warm = Math.floor(n / 10);
for (let i = 0; i < warm; i++) {
  if (normalize(inputs[i]) !== null) sink++;
}

const t0 = performance.now();
let ok = 0;
let same = 0;
for (let i = 0; i < n; i++) {
  const u = normalize(inputs[i]);
  if (u !== null) {
    ok++;
    if (sameDomain(start, u)) same++;
  }
}
const ms = performance.now() - t0;

console.log(JSON.stringify({
  lang: 'ts',
  runtime: `node ${process.versions.node}`,
  n,
  ms: Math.round(ms * 100) / 100,
  nsPerOp: Math.round((ms * 1e6) / n),
  ok,
  same,
  sink,
}));
