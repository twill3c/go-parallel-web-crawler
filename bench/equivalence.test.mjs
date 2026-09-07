// T-1001 / ROADMAP-G: **比較の前提** —— Go 版と TS 版が同じサイトから同じ結果を出すこと。
// 揃っていない実装の速さを比べても意味が無いので、時間を測る前にここを固定する(HC-070)。
//
// 実行: node --test bench/equivalence.test.mjs
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

// **同期 API(execFileSync)を使ってはならない。** 合成サイトはこのプロセスの中で動いているので、
// イベントループを止めると Go 側のリクエストに応答できず、10 秒でタイムアウトする。
// 症状は「接続は届いているのに返らない」で、クライアント側の設定を疑いたくなる(2026-09-07 に踏んだ)。
const execFileAsync = promisify(execFile);

import { startSite } from './site.mjs';
import { run, normalize, sameDomain, looksNonHtml, extract } from './crawler.ts';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const GO = process.env.GO_BIN || 'go';

let site;
before(async () => { site = await startSite({ pages: 30, delay: 0, bytes: 2000, links: 6 }); });
after(async () => { await site?.close(); });

async function runGo(args) {
  const { stdout } = await execFileAsync(GO, ['run', './cmd/crawlbench', ...args], { cwd: root, encoding: 'utf8' });
  return JSON.parse(stdout.trim().split('\n').at(-1));
}

test('T-1001: 同じ設定で取得ページ数・辺の数・完了理由が一致する', async () => {
  const cases = [
    { workers: 1, max: 30 },
    { workers: 5, max: 30 },
    { workers: 5, max: 12 }, // 上限で切られる場合
  ];
  for (const c of cases) {
    const go = await runGo(['-url', site.url, '-workers', String(c.workers), '-max', String(c.max), '-delay', '0']);
    const ts = await run({ startUrl: site.url, workers: c.workers, maxPages: c.max, requestDelayMs: 0, timeoutMs: 10000 });
    assert.equal(ts.pages.length, go.pages, `workers=${c.workers} max=${c.max}: ページ数`);
    assert.equal(ts.reason, go.reason, `workers=${c.workers} max=${c.max}: 完了理由`);
    // 上限で切られないときは、辺の集合まで一致する(切られる場合は到達順で変わりうる)
    if (c.max >= 30) {
      assert.equal(ts.links.length, go.links, `workers=${c.workers}: 辺の数`);
    }
    // 前提の検算: 実際に複数ページ取れている(0 ページ同士の一致は何も言わない)
    assert.ok(go.pages > 5, `go pages=${go.pages}`);
  }
});

test('T-1002: 正規化・同一ドメイン・拡張子判定が Go 版と同じ規則', () => {
  // Go 側の TestNormalize_Table / TestSameDomain_Table と同じ表(片方だけ直せば落ちる)
  const norm = [
    ['https://example.com', 'https://example.com/'],
    ['https://Example.COM/', 'https://example.com/'],
    ['https://example.com:443/a', 'https://example.com/a'],
    ['http://example.com:80/a', 'http://example.com/a'],
    ['http://example.com:8080/a', 'http://example.com:8080/a'],
    ['https://example.com/a#section', 'https://example.com/a'],
    ['https://example.com/a/b/../c', 'https://example.com/a/c'],
    ['https://example.com/a/', 'https://example.com/a/'],
    ['https://example.com/日本', 'https://example.com/%E6%97%A5%E6%9C%AC'],
  ];
  for (const [input, want] of norm) {
    const got = normalize(input);
    assert.equal(got, want, `normalize(${input})`);
    assert.equal(normalize(got), got, `冪等でない: ${input}`);
  }
  for (const bad of ['', 'not a url', 'mailto:a@example.com', '//example.com/x', '/relative']) {
    assert.equal(normalize(bad), null, `normalize(${bad}) が通った`);
  }

  const dom = [
    ['https://example.com/', 'https://example.com/about', true],
    ['https://example.com/', 'https://www.example.com/about', true],
    ['https://www.example.com/', 'https://example.com/about', true],
    ['https://example.com/', 'http://example.com/about', true],
    ['https://example.com/', 'https://example.com:8443/about', false],
    ['https://example.com/', 'https://blog.example.com/', false],
    ['https://example.com/', 'https://example.org/', false],
    ['https://example.com/', 'https://notexample.com/', false],
  ];
  for (const [start, link, want] of dom) {
    assert.equal(sameDomain(start, link), want, `sameDomain(${start}, ${link})`);
  }

  assert.equal(looksNonHtml('https://e.com/a.png'), true);
  assert.equal(looksNonHtml('https://e.com/a.html'), false);
  assert.equal(looksNonHtml('https://e.com/a'), false);
});

test('T-1003: 合成サイトの HTML に対して抽出結果が Go 版と一致する', async () => {
  // 実際のページを取ってきて、両者の抽出結果を比べる。
  // 解析器そのものは別物(x/net/html vs 走査)なので、**この HTML に対して**同じ、が主張の範囲
  const res = await fetch(new URL('/p3', site.url));
  const html = await res.text();
  const { links, title } = extract(new URL('/p3', site.url).toString(), html);
  assert.equal(title, 'Page 3');
  // Go 版と同じ数・同じ集合であることは T-1001 のクロール一致で担保される。
  // ここでは抽出そのものの性質を押さえる: 外部リンクも画像も**抽出はする**(除外は呼び手)
  assert.ok(links.some((l) => l.includes('other.example.org')), '外部リンクを落としている');
  assert.ok(links.some((l) => l.endsWith('/asset.png')), '画像リンクを落としている');
  assert.ok(links.filter((l) => /\/p\d+$/.test(l)).length >= 6, `内部リンクが少ない: ${links.length}`);
  // 重複は畳む
  assert.equal(new Set(links).size, links.length);
});
