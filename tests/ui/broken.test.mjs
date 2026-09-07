// T-906 / ROADMAP-D: 画面側の状態区分と参照元の逆引き。
// 期待値の出所: SPEC F-33 と Go 側の StatusClass(同じ規則を二か所に書いたので、
// 表を揃えて両方から確かめる ―― 片方だけ直したときに気づけるようにするため)。
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { statusClass, STATUS_SYMBOL, referrers, brokenPages, initialState, reduce } from '../../public/state.js';

// Go の TestStatusClass と同じ表。どちらかを直したらもう一方も落ちる
const TABLE = [
  [200, '', 'ok'],
  [204, '', 'ok'],
  [301, '', 'redirect'],
  [404, 'HTTP 404', 'missing'],
  [403, 'HTTP 403', 'missing'],
  [500, 'HTTP 500', 'server'],
  [503, 'HTTP 503', 'server'],
  [0, 'Timeout', 'failed'],
  [0, 'DNS Error', 'failed'],
  [200, 'Non HTML', 'other'],
  [0, 'Cancelled', 'other'],
];

test('statusClass は Go 側と同じ区分を返す', () => {
  for (const [status, error, want] of TABLE) {
    assert.equal(statusClass(status, error), want, `${status} / ${error}`);
  }
});

test('区分ごとに記号と読みがある(色が無くても区別できる)', () => {
  const classes = [...new Set(TABLE.map((r) => r[2]))];
  for (const c of classes) {
    assert.ok(STATUS_SYMBOL[c], `${c} の記号が無い`);
    assert.ok(STATUS_SYMBOL[c].sym.length > 0);
    assert.ok(STATUS_SYMBOL[c].label.length > 0);
  }
  // 記号だけで一意でなくてよいが(✕ は missing と failed で共用)、読みは区別できること
  const labels = classes.map((c) => STATUS_SYMBOL[c].label);
  assert.equal(new Set(labels).size, labels.length, '読みが重複している');
});

// 状態を組み立てるための最小のイベント列
function build(events) {
  let s = initialState();
  for (const e of events) s = reduce(s, e);
  return s;
}

test('referrers は壊れた URL を指しているページを漏れなく返す', () => {
  const s = build([
    { type: 'crawl_started', workers: 1, maxPages: 10, url: 'https://e.com/' },
    { type: 'page_completed', workerId: 1, url: 'https://e.com/', statusCode: 200 },
    { type: 'link_found', from: 'https://e.com/', to: 'https://e.com/gone', queued: true },
    { type: 'link_found', from: 'https://e.com/', to: 'https://e.com/a', queued: true },
    { type: 'page_completed', workerId: 1, url: 'https://e.com/a', statusCode: 200 },
    { type: 'link_found', from: 'https://e.com/a', to: 'https://e.com/gone', queued: false },
    { type: 'page_completed', workerId: 1, url: 'https://e.com/gone', statusCode: 404, error: 'HTTP 404' },
  ]);
  assert.deepEqual(referrers(s, 'https://e.com/gone'), ['https://e.com/', 'https://e.com/a']);
  assert.deepEqual(referrers(s, 'https://e.com/a'), ['https://e.com/']);
  assert.deepEqual(referrers(s, 'https://e.com/none'), []);

  const broken = brokenPages(s);
  assert.equal(broken.length, 1);
  assert.equal(broken[0].url, 'https://e.com/gone');
  assert.equal(broken[0].class, 'missing');
  assert.equal(broken[0].from.length, 2);
});

test('陽性対照: 壊れたページが無ければ brokenPages は空', () => {
  const s = build([
    { type: 'crawl_started', workers: 1, maxPages: 10, url: 'https://e.com/' },
    { type: 'page_completed', workerId: 1, url: 'https://e.com/', statusCode: 200 },
  ]);
  assert.deepEqual(brokenPages(s), []);
  // かつ、200 のページが missing に紛れ込まないこと(区分の取り違えを捕まえる)
  assert.equal(statusClass(200, ''), 'ok');
});

test('書誌情報(description / h1 / canonical / finalUrl)がイベントから状態へ渡る', () => {
  const s = build([
    { type: 'crawl_started', workers: 1, maxPages: 10, url: 'https://e.com/' },
    {
      type: 'page_completed', workerId: 1, url: 'https://e.com/', statusCode: 200,
      title: '題', description: '説明', h1: '見出し', canonical: 'https://e.com/canon', finalUrl: 'https://e.com/final',
    },
  ]);
  const p = s.pages[0];
  assert.equal(p.description, '説明');
  assert.equal(p.h1, '見出し');
  assert.equal(p.canonical, 'https://e.com/canon');
  assert.equal(p.finalUrl, 'https://e.com/final');
  // 無いイベントでは空文字(undefined を画面に出さない)
  const s2 = build([
    { type: 'crawl_started', workers: 1, maxPages: 10, url: 'https://e.com/' },
    { type: 'page_completed', workerId: 1, url: 'https://e.com/x', statusCode: 200 },
  ]);
  assert.equal(s2.pages[0].description, '');
  assert.equal(s2.pages[0].canonical, '');
});
