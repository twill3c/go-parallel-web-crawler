// T-401 / G-09: SSE の一括到着と 1 文字ずつの到着で、パーサが出すイベント列と reducer の最終状態が一致する。
// 期待値の出所: SPEC F-36 / G-09。フィクスチャは Go のテストが実サーバから記録した SSE(tests/ui/fixtures/*.sse)。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import { createSSEParser, initialState, reduce } from '../../public/state.js';

const here = dirname(fileURLToPath(import.meta.url));
const fixtureDir = join(here, 'fixtures');
const fixtures = readdirSync(fixtureDir).filter((f) => f.endsWith('.sse'));

function replay(text, chunker) {
  const events = [];
  const parser = createSSEParser((e) => events.push(e));
  for (const chunk of chunker(text)) parser.push(chunk);
  parser.end();
  let state = initialState();
  for (const e of events) state = reduce(state, e);
  return { events, state };
}

const whole = (t) => [t];
const perChar = (t) => Array.from(t);
const perLine = (t) => t.split(/(?<=\n)/);

test('フィクスチャが存在する(走査対象が空でない — HC-041)', () => {
  assert.ok(fixtures.length > 0, 'tests/ui/fixtures/*.sse が無い');
});

for (const f of fixtures) {
  const text = readFileSync(join(fixtureDir, f), 'utf8');
  test(`${f}: 一括 / 1 文字ずつ / 行ごと で同じイベント列と最終状態`, () => {
    const a = replay(text, whole);
    const b = replay(text, perChar);
    const c = replay(text, perLine);
    assert.ok(a.events.length >= 3, 'イベントが少なすぎる');
    assert.deepEqual(b.events, a.events);
    assert.deepEqual(c.events, a.events);
    assert.deepEqual(b.state, a.state);
    assert.deepEqual(c.state, a.state);
  });

  test(`${f}: 最終状態が crawl_completed の統計と整合する(G-08 の画面側)`, () => {
    const { events, state } = replay(text, whole);
    assert.equal(events[0].type, 'crawl_started');
    const last = events[events.length - 1];
    assert.equal(last.type, 'crawl_completed');
    assert.equal(state.status, 'completed');
    assert.equal(state.pages.length, last.statistics.total);
    assert.equal(state.pages.filter((p) => !p.error).length, last.statistics.success);
    assert.equal(state.pages.filter((p) => p.error).length, last.statistics.errors);
    // Worker 行の数 = workers、全員 completed
    assert.equal(Object.keys(state.workers).length, events[0].workers);
    for (const w of Object.values(state.workers)) assert.equal(w.status, 'completed');
    // ノード集合 ⊇ 取得ページ集合、かつ取得済みノード数 = pages 数(集合として一致)
    const fetched = [...state.nodes.values()].filter((n) => n.fetched).map((n) => n.url).sort();
    assert.deepEqual(fetched, state.pages.map((p) => p.url).sort());
    // 辺は一意
    const keys = state.links.map((l) => `${l.from} ${l.to}`);
    assert.equal(new Set(keys).size, keys.length);
  });
}

test('陽性対照: 壊れた data 行は捨て、後続のイベントは生きる', () => {
  const events = [];
  const p = createSSEParser((e) => events.push(e));
  p.push('event: crawl_started\ndata: {not json\n\nevent: worker_done\ndata: {"type":"worker_done","workerId":1,"t":5}\n\n');
  p.end();
  assert.deepEqual(events.map((e) => e.type), ['worker_done']);
});

test('陽性対照: 末尾に空行が無くても end() で最後のイベントが出る', () => {
  const events = [];
  const p = createSSEParser((e) => events.push(e));
  p.push('data: {"type":"crawl_completed","t":9,"reason":"exhausted","statistics":{"total":0}}');
  p.end();
  assert.equal(events.length, 1);
});
