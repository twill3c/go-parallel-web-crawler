// bench/tscrawl.ts — TS 版クローラを 1 回走らせて JSON を出す(cmd/crawlbench の対).
// 起動時間を測定に含めないため、時間は run() の中で測る(Go 側と同じ扱い)。
import { run } from './crawler.ts';

function arg(name: string, fallback: string): string {
  const i = process.argv.indexOf(`-${name}`);
  return i >= 0 && i + 1 < process.argv.length ? process.argv[i + 1] : fallback;
}

const url = arg('url', '');
if (url === '') {
  console.error('-url is required');
  process.exit(2);
}

const res = await run({
  startUrl: url,
  workers: Number(arg('workers', '5')),
  maxPages: Number(arg('max', '30')),
  requestDelayMs: Number(arg('delay', '0')),
  timeoutMs: 10000,
});

const success = res.pages.filter((p) => p.error === '').length;
console.log(JSON.stringify({
  lang: 'ts',
  wallMs: res.wallMs,
  pages: res.pages.length,
  links: res.links.length,
  reason: res.reason,
  fetchMs: res.fetchMs,
  parseMs: res.parseMs,
  success,
  errors: res.pages.length - success,
  firstError: res.pages.find((p) => p.error !== '')?.error ?? '',
}));
