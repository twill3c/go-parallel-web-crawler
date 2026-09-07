// bench/run.mjs — Go 版と TS 版を同じ条件で走らせ、public/bench-results.json を書く(ROADMAP G)。
//
//   node bench/run.mjs [--reps 5] [--out public/bench-results.json]
//
// 測り方の約束:
//   - 同じ合成サイトを相手にする(片方だけ有利な条件を作らない)
//   - **交互に走らせる**(go, ts, go, ts, …)。連続して走らせると、機械の温まりや
//     他プロセスの影響が片方に偏る
//   - 起動時間は測らない。時間はどちらもプロセスの内側で測る
//   - 代表値は**中央値**。平均は 1 回の外れ値に引きずられる
//   - 条件(機械・版・日付・引数)を結果と一緒に書く。**条件の無い数は読めない**
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { writeFileSync, mkdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join } from 'node:path';
import { cpus, totalmem, release } from 'node:os';

import { startSite } from './site.mjs';

// **同期 API を使ってはならない** —— 合成サイトはこのプロセスで動いており、
// イベントループを止めると応答できなくなる(2026-09-07 に踏んだ)
const execFileAsync = promisify(execFile);

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const GO = process.env.GO_BIN || 'go';

function argOf(name, fallback) {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 && i + 1 < process.argv.length ? process.argv[i + 1] : fallback;
}
const REPS = Number(argOf('reps', '5'));
const OUT = argOf('out', join(root, 'public', 'bench-results.json'));

const median = (xs) => {
  const s = [...xs].sort((a, b) => a - b);
  const m = Math.floor(s.length / 2);
  return s.length % 2 ? s[m] : Math.round((s[m - 1] + s[m]) / 2);
};
const spread = (xs) => ({ min: Math.min(...xs), max: Math.max(...xs) });

// MIN_BENCH_MS は「比べてよい」と言うための下限(画面のベンチと同じ規律・G-13)。
// これより速い計測は、差が実装でなく機械のばらつきで決まる。
// 実測(2026-09-07): 同じ条件を二度回して URL 正規化の比が 1.96 → 2.97 に動いた。
const MIN_BENCH_MS = 500;

async function build() {
  const bin = join(root, 'bench', 'out', process.platform === 'win32' ? 'crawlbench.exe' : 'crawlbench');
  const ubin = join(root, 'bench', 'out', process.platform === 'win32' ? 'urlbench.exe' : 'urlbench');
  mkdirSync(dirname(bin), { recursive: true });
  await execFileAsync(GO, ['build', '-o', bin, './cmd/crawlbench'], { cwd: root });
  await execFileAsync(GO, ['build', '-o', ubin, './cmd/urlbench'], { cwd: root });
  return { bin, ubin };
}

const runJson = async (cmd, args) => {
  const { stdout } = await execFileAsync(cmd, args, { cwd: root, encoding: 'utf8', maxBuffer: 8 << 20 });
  return JSON.parse(stdout.trim().split('\n').at(-1));
};

/** crawlCase は 1 つの条件で両実装を交互に reps 回走らせ、中央値を返す。 */
async function crawlCase({ bin, label, pages, delay, bytes, links, workers, maxPages }) {
  const site = await startSite({ pages, delay, bytes, links });
  try {
    const go = [];
    const ts = [];
    const goW1 = [];
    let goOut = null;
    let tsOut = null;
    for (let i = 0; i < REPS; i++) {
      const a = ['-url', site.url, '-workers', String(workers), '-max', String(maxPages), '-delay', '0'];
      goOut = await runJson(bin, a);
      go.push(goOut.wallMs);
      tsOut = await runJson(process.execPath, [join(root, 'bench', 'tscrawl.ts'), ...a]);
      ts.push(tsOut.wallMs);
      // 同じ条件を Go の Workers=1 でも測る。**合成サイトが律速なら並行しても速くならない**ので、
      // この比が 1 に近い行は「クローラでなくサイトを測っている」と分かる(交絡の検出器)
      const w1 = await runJson(bin, ['-url', site.url, '-workers', '1', '-max', String(maxPages), '-delay', '0']);
      goW1.push(w1.wallMs);
    }
    // 比較が成り立つ前提: 両者が同じだけ取れていること。崩れていたら数を出さない
    const sameResult = goOut.pages === tsOut.pages && goOut.reason === tsOut.reason;
    // 並行の効き(Workers=1 に対する倍率)。workers=1 の行では常に 1 になるので判定から外す
    const goScaling = Math.round((median(goW1) / Math.max(1, median(go))) * 100) / 100;
    // 合成サイトは 1 スレッドの Node なので、速く返しすぎると**サイトが律速**になる。
    // Workers を N 倍にしても N 倍にならない行は、クローラでなくサイトを測っている
    const siteBound = workers > 1 && goScaling < workers * 0.6;
    // 速すぎる計測は信用しない(G-13 と同じ下限)
    const noisy = median(go) < MIN_BENCH_MS || median(ts) < MIN_BENCH_MS;
    const comparable = sameResult && !siteBound && !noisy;
    return {
      label,
      params: { pages, delayMs: delay, bytes, links, workers, maxPages },
      comparable,
      sameResult,
      siteBound,
      noisy,
      goScaling, // Workers=1 に対する Go の速度倍率。workers に近ければ本当に並行できている
      pages: { go: goOut.pages, ts: tsOut.pages },
      reason: { go: goOut.reason, ts: tsOut.reason },
      wallMs: { go: median(go), ts: median(ts), goW1: median(goW1) },
      spreadMs: { go: spread(go), ts: spread(ts) },
      wallAll: { go, ts, goW1 },
      fetchMs: { go: goOut.fetchMs, ts: tsOut.fetchMs },
      parseMs: { ts: tsOut.parseMs }, // Go 側は解析時間を分けて測っていない(取得と一体)
      ratio: Math.round((median(ts) / Math.max(1, median(go))) * 100) / 100,
    };
  } finally {
    await site.close();
  }
}

const cases = [
  // 遅延あり: 実際のサイトに近い。ネットワーク待ちが支配するはず
  { label: '遅延 50ms・60 ページ・Workers 5', pages: 60, delay: 50, bytes: 4000, links: 8, workers: 5, maxPages: 60 },
  { label: '遅延 50ms・30 ページ・Workers 1', pages: 30, delay: 50, bytes: 4000, links: 8, workers: 1, maxPages: 30 },
  // 遅延なし: 待ちを取り除くと、残るのは取得の手続きと解析。
  // 下限(500ms)を超えるだけの仕事を積むためページ数を増やす
  { label: '遅延 0・400 ページ・Workers 5', pages: 400, delay: 0, bytes: 4000, links: 8, workers: 5, maxPages: 400 },
  // 大きい本文: 解析の比重を上げる
  { label: '遅延 0・150 ページ・本文 60KB・Workers 5', pages: 150, delay: 0, bytes: 60000, links: 8, workers: 5, maxPages: 150 },
];

const { bin, ubin } = await build();

const crawl = [];
for (const c of cases) {
  process.stderr.write(`… ${c.label}\n`);
  crawl.push(await crawlCase({ bin, ...c }));
}

process.stderr.write('… URL 正規化\n');
const N = 200000;
const urlGo = [];
const urlTs = [];
let goU = null;
let tsU = null;
for (let i = 0; i < REPS; i++) {
  goU = await runJson(ubin, ['-n', String(N)]);
  urlGo.push(goU.ms);
  tsU = await runJson(process.execPath, [join(root, 'bench', 'urlbench.ts'), String(N)]);
  urlTs.push(tsU.ms);
}

const results = {
  measuredAt: new Date().toISOString(),
  conditions: {
    machine: `${cpus()[0]?.model ?? 'unknown'} / ${cpus().length} 論理コア / RAM ${Math.round(totalmem() / 1e9)}GB`,
    os: `${process.platform} ${release()}`,
    go: goU.runtime,
    node: tsU.runtime,
    reps: REPS,
    note: '合成サイトは同一プロセス(Node)で動かし、両実装が同じ相手を叩く。時間はプロセスの内側で測り、起動時間を含めない。代表値は中央値',
  },
  crawl,
  urlNormalize: {
    n: N,
    msGo: median(urlGo),
    msTs: median(urlTs),
    spreadMs: { go: spread(urlGo), ts: spread(urlTs) },
    ratio: Math.round((median(urlTs) / Math.max(1, median(urlGo))) * 100) / 100,
    // 同じ入力から同じ件数が出ていること。ここが崩れたら速さの比較は無意味
    okMatches: goU.ok === tsU.ok && goU.same === tsU.same,
    counts: { ok: goU.ok, same: goU.same },
    noisy: median(urlGo) < MIN_BENCH_MS || median(urlTs) < MIN_BENCH_MS,
  },
};

mkdirSync(dirname(OUT), { recursive: true });
writeFileSync(OUT, JSON.stringify(results, null, 2) + '\n', 'utf8');
process.stderr.write(`書いた: ${OUT}\n`);
console.log(JSON.stringify(results, null, 2));
