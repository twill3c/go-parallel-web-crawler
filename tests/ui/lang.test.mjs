// T-1010 / ROADMAP-G: 言語比較の表示ロジック。**比較が成立していない行で倍率を出さない**ことを固定する。
// 期待値の出所: SPEC G-16 と、実際に書き出された public/bench-results.json の形。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join, resolve } from 'node:path';

import { langRows, langVerdict, langCrossRun } from '../../public/state.js';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');
const data = JSON.parse(readFileSync(join(root, 'public', 'bench-results.json'), 'utf8'));

test('実測ファイルが条件を持っている(条件の無い数は読めない)', () => {
  for (const k of ['machine', 'os', 'go', 'node', 'reps', 'note']) {
    assert.ok(data.conditions?.[k], `conditions.${k} が無い`);
  }
  assert.ok(data.measuredAt, 'measuredAt が無い');
  assert.ok(data.crawl.length >= 2, '条件が少なすぎる');
});

test('成立した行だけが倍率を持ち、成立しない行は理由を持つ', () => {
  const rows = langRows(data);
  assert.equal(rows.length, data.crawl.length);
  let comparable = 0;
  for (const r of rows) {
    if (r.comparable) {
      comparable++;
      assert.equal(typeof r.ratio, 'number');
      assert.equal(r.whyNot, '', `成立しているのに理由が付いている: ${r.label}`);
    } else {
      assert.equal(r.ratio, null, `成立していないのに倍率が出ている: ${r.label}`);
      assert.ok(r.whyNot.length > 0, `理由が空: ${r.label}`);
    }
  }
  // 陽性対照: 少なくとも 1 条件は成立している(全滅なら測定そのものが失敗)
  assert.ok(comparable > 0, '比較できた条件が 0 —— 測定が成立していない');
});

test('陽性対照: 交絡している行を作ると倍率が消える', () => {
  const base = structuredClone(data);
  const row = base.crawl.find((c) => c.comparable);
  assert.ok(row, '成立している行が無い');
  // (a) サイトが律速
  row.comparable = false; row.siteBound = true;
  assert.equal(langRows(base)[base.crawl.indexOf(row)].ratio, null);
  assert.match(langRows(base)[base.crawl.indexOf(row)].whyNot, /律速/);
  // (b) 短すぎる
  row.siteBound = false; row.noisy = true;
  assert.match(langRows(base)[base.crawl.indexOf(row)].whyNot, /短すぎる/);
  // (c) 結果が食い違う
  row.noisy = false; row.sameResult = false;
  assert.match(langRows(base)[base.crawl.indexOf(row)].whyNot, /食い違/);
  // (d) ばらつきが大きい(同じ条件で最大 / 最小が 2 倍を超える)
  row.sameResult = true; row.unstable = true; row.swing = { go: 3.4, ts: 1.1 };
  const r = langRows(base)[base.crawl.indexOf(row)];
  assert.equal(r.ratio, null);
  assert.match(r.whyNot, /ばらつき/);
  assert.match(r.whyNot, /3\.4 倍/);
});

test('langVerdict は成立した行だけを使う', () => {
  const v = langVerdict(data);
  assert.ok(v, '判定文が出ていない');
  const comparable = langRows(data).filter((r) => r.comparable);
  assert.equal(v.rows, comparable.length);
  assert.equal(v.lo, Math.min(...comparable.map((r) => r.ratio)));
  // 成立した行が無ければ null(数を出さない)
  const empty = structuredClone(data);
  for (const c of empty.crawl) c.comparable = false;
  assert.equal(langVerdict(empty), null);
  assert.equal(langVerdict(null), null);
});

// T-1012 / G-16: 実行をまたいだ再現性。**実行内の安定は再現性ではない。**
test('langCrossRun は実行間で動いた条件を「再現しなかった」に分ける', () => {
  assert.equal(langCrossRun(null), null);
  assert.equal(langCrossRun({ crossRun: { runs: 1, ratios: {} } }), null, '1 回では範囲が出せない');

  const cr = langCrossRun({
    crossRun: {
      runs: 2,
      ratios: {
        '安定した条件': { n: 2, min: 0.99, max: 1.08 },
        '動いた条件': { n: 2, min: 1.7, max: 3.76 },
      },
      urlRatios: [2.99, 1.52],
    },
  });
  assert.equal(cr.runs, 2);
  assert.deepEqual(cr.reproduced.map((r) => r.label), ['安定した条件']);
  assert.deepEqual(cr.notReproduced.map((r) => r.label), ['動いた条件']);
  // 幅は max/min。1.2 倍が境目
  assert.equal(cr.reproduced[0].width, 1.09);
  assert.equal(cr.notReproduced[0].width, 2.21);
  assert.deepEqual(cr.urlRatios, [2.99, 1.52]);
});

test('実測ファイルに履歴があれば、再現しなかった条件を数えられる', () => {
  const cr = langCrossRun(data);
  if (cr === null) return; // 履歴が 1 回分しかない実測ファイルもある
  assert.ok(cr.rows.length > 0);
  for (const r of cr.rows) {
    assert.ok(r.min <= r.max);
    assert.ok(r.n >= 1);
  }
});

test('URL 正規化の測定は両実装の出力件数が一致している', () => {
  const u = data.urlNormalize;
  assert.equal(u.okMatches, true, '出力件数が食い違ったまま速さを比べている');
  assert.ok(u.n >= 100000);
  assert.equal(u.noisy, false, '計測が短すぎる');
});
