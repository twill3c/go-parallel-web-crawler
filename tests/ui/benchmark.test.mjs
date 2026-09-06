// T-601 / F-38: ベンチマークの集計(純関数)。速度比・棒の長さ・表示の丸めを検査する。
// 期待値の出所: SPEC F-38 と §5(Requests/sec = total / (durationMs/1000))。
// 手で計算できる合成データを使い、期待値は式から導出する(定数を二重に書かない)。
import { test } from 'node:test';
import assert from 'node:assert/strict';

import { BENCH_WORKERS, benchRow, benchSummary, MIN_BENCH_MS } from '../../public/state.js';

test('BENCH_WORKERS は昇順で、上限 20 を超えない(SPEC §2.5)', () => {
  assert.ok(BENCH_WORKERS.length >= 3);
  const sorted = [...BENCH_WORKERS].sort((a, b) => a - b);
  assert.deepEqual(BENCH_WORKERS, sorted);
  assert.ok(BENCH_WORKERS[0] >= 1);
  assert.ok(BENCH_WORKERS.at(-1) <= 20);
});

test('benchRow は crawl_completed の statistics から 1 行を作る', () => {
  const st = { total: 20, success: 19, errors: 1, durationMs: 2000, requestsPerSec: 10, avgMs: 90, p95Ms: 300 };
  const row = benchRow(5, st, 'max_pages');
  assert.equal(row.workers, 5);
  assert.equal(row.pages, 20);
  assert.equal(row.errors, 1);
  assert.equal(row.seconds, 2); // durationMs / 1000
  assert.equal(row.pagesPerSec, 10); // total / seconds、サーバの値と一致する
  assert.equal(row.reason, 'max_pages');
});

test('benchRow はサーバの requestsPerSec を使わず自分で割っても同じ値になる(二経路)', () => {
  for (const [total, durationMs] of [[8, 2010], [50, 3333], [1, 435]]) {
    const rps = Math.round((total / (durationMs / 1000)) * 10) / 10;
    const row = benchRow(1, { total, durationMs, requestsPerSec: rps, errors: 0, success: total, avgMs: 0, p95Ms: 0 }, 'exhausted');
    assert.equal(row.pagesPerSec, rps);
  }
});

test('benchSummary は最速行・速度比・棒の割合を出す', () => {
  const rows = [
    benchRow(1, { total: 20, durationMs: 8000, errors: 0, success: 20, requestsPerSec: 2.5, avgMs: 0, p95Ms: 0 }, 'max_pages'),
    benchRow(2, { total: 20, durationMs: 4000, errors: 0, success: 20, requestsPerSec: 5, avgMs: 0, p95Ms: 0 }, 'max_pages'),
    benchRow(5, { total: 20, durationMs: 2000, errors: 0, success: 20, requestsPerSec: 10, avgMs: 0, p95Ms: 0 }, 'max_pages'),
    benchRow(10, { total: 20, durationMs: 1900, errors: 0, success: 20, requestsPerSec: 10.5, avgMs: 0, p95Ms: 0 }, 'max_pages'),
  ];
  const s = benchSummary(rows);
  assert.equal(s.maxPagesPerSec, 10.5);
  assert.equal(s.fastest.workers, 10);
  assert.equal(s.baseline.workers, 1);
  // 速度比は Workers=1 に対する倍率
  assert.deepEqual(rows.map((r) => s.speedup(r)), [1, 2, 4, 4.2]);
  // 棒は最大値に対する割合(0..1)
  assert.deepEqual(rows.map((r) => s.bar(r)), [2.5 / 10.5, 5 / 10.5, 10 / 10.5, 1]);
  // 比較が成り立つ前提: 全行が同じページ数を取れている(取れていなければ時間の比較に意味が無い)
  assert.equal(s.comparable, true);
  // かつ各行が MIN_BENCH_MS 以上かかっている(この行は 1900〜8000 ms なので満たす)
  assert.equal(s.longEnough, true);
  assert.equal(s.trustworthy, true);
});

test('短すぎる計測は longEnough=false(差が並行度でなく往復のばらつき)', () => {
  // 実測(2026-09-07・ローカル合成サイト)で出た形: 全行 20〜50 ms、値が単調ですらない
  const rows = [
    benchRow(1, { total: 12, durationMs: 27, errors: 1, success: 11, requestsPerSec: 444.4, avgMs: 1, p95Ms: 5 }, 'max_pages'),
    benchRow(2, { total: 12, durationMs: 48, errors: 1, success: 11, requestsPerSec: 250, avgMs: 3, p95Ms: 21 }, 'max_pages'),
    benchRow(5, { total: 12, durationMs: 16, errors: 1, success: 11, requestsPerSec: 750, avgMs: 1, p95Ms: 6 }, 'max_pages'),
    benchRow(10, { total: 12, durationMs: 45, errors: 1, success: 11, requestsPerSec: 266.7, avgMs: 3, p95Ms: 24 }, 'max_pages'),
  ];
  const s = benchSummary(rows);
  // ページ数は揃っているので comparable は真。それでも短すぎるので信用しない
  assert.equal(s.comparable, true);
  assert.equal(s.longEnough, false);
  assert.equal(s.trustworthy, false);
  // 前提の検算: この標本が実際に「短い」こと(MIN_BENCH_MS を下回る行がある)
  assert.ok(rows.some((r) => r.durationMs < MIN_BENCH_MS));
});

test('境界: 全行がちょうど MIN_BENCH_MS なら longEnough=true', () => {
  const mk = (w) => benchRow(w, { total: 5, durationMs: MIN_BENCH_MS, errors: 0, success: 5, requestsPerSec: 10, avgMs: 0, p95Ms: 0 }, 'exhausted');
  assert.equal(benchSummary([mk(1), mk(2)]).longEnough, true);
  const short = benchRow(2, { total: 5, durationMs: MIN_BENCH_MS - 1, errors: 0, success: 5, requestsPerSec: 10, avgMs: 0, p95Ms: 0 }, 'exhausted');
  assert.equal(benchSummary([mk(1), short]).longEnough, false);
});

test('ページ数が揃わない行があれば comparable=false(比較の前提が崩れている)', () => {
  const rows = [
    benchRow(1, { total: 20, durationMs: 8000, errors: 0, success: 20, requestsPerSec: 2.5, avgMs: 0, p95Ms: 0 }, 'max_pages'),
    benchRow(5, { total: 12, durationMs: 2000, errors: 0, success: 12, requestsPerSec: 6, avgMs: 0, p95Ms: 0 }, 'cancelled'),
  ];
  assert.equal(benchSummary(rows).comparable, false);
});

test('陰性対照: 行が 0 件・1 件でも落ちない', () => {
  const empty = benchSummary([]);
  assert.equal(empty.fastest, null);
  assert.equal(empty.comparable, false);
  assert.equal(empty.trustworthy, false);
  const one = benchSummary([benchRow(1, { total: 5, durationMs: 1000, errors: 0, success: 5, requestsPerSec: 5, avgMs: 0, p95Ms: 0 }, 'exhausted')]);
  assert.equal(one.fastest.workers, 1);
  assert.equal(one.bar(one.fastest), 1);
  assert.equal(one.speedup(one.fastest), 1);
  assert.equal(one.trustworthy, false); // 1 行では比較にならない
});

test('陽性対照: durationMs が 0 の行でも無限大を出さない', () => {
  const row = benchRow(1, { total: 3, durationMs: 0, errors: 0, success: 3, requestsPerSec: 0, avgMs: 0, p95Ms: 0 }, 'exhausted');
  assert.equal(Number.isFinite(row.pagesPerSec), true);
  assert.equal(row.pagesPerSec, 0);
  const s = benchSummary([row]);
  assert.equal(Number.isFinite(s.bar(row)), true);
});
