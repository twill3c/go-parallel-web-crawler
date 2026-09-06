// scripts/probe_stream.mjs — G-11 / T-502: 本番の SSE が逐次届くかを測る。
// 使い方: node scripts/probe_stream.mjs https://go-parallel-web-crawler.vercel.app https://<対象>/
// 判定: firstChunkMs が lastChunkMs より十分小さく chunks が 1 より多ければ、ストリーミングが効いている。
// G-11 / T-502: 本番へ POST /api/crawl し、各チャンクの到着時刻を記録する。
const url = process.argv[2];
const target = process.argv[3];
const t0 = performance.now();
const res = await fetch(`${url}/api/crawl`, {
  method: 'POST', headers: { 'content-type': 'application/json' },
  body: JSON.stringify({ url: target, workers: 2, maxPages: 8, requestDelayMs: 300 }),
});
console.log('status', res.status, res.headers.get('content-type'));
const reader = res.body.getReader();
const dec = new TextDecoder();
let chunks = 0, first = null, last = null, text = '';
for (;;) {
  const { value, done } = await reader.read();
  if (done) break;
  const t = performance.now() - t0;
  if (first === null) first = t;
  last = t;
  chunks++;
  text += dec.decode(value, { stream: true });
}
const events = [...text.matchAll(/^event: (\w+)/gm)].map((m) => m[1]);
console.log(JSON.stringify({ chunks, firstChunkMs: Math.round(first), lastChunkMs: Math.round(last), events: events.length, firstEvent: events[0], lastEvent: events.at(-1) }));
const completed = JSON.parse(text.split('\n').filter((l) => l.startsWith('data:')).at(-1).slice(5));
console.log('reason', completed.reason, 'stats', JSON.stringify(completed.statistics));
