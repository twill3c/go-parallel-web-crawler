// T-402 / G-10, T-403 / F-37: 実ブラウザ検品(HC-138: 在存でなく幾何と到達を測る)。
//
//   合成サイト(Node の http)←── Go サーバ(GPWC_ALLOW_PRIVATE=1・PORT 任意)←── Chromium(Playwright)
//
// 実行: node --test tests/ui/browser.test.mjs
// Playwright は同梱しない。PLAYWRIGHT_DIR(既定: ../hacchu-forge/node_modules/playwright)から借りる。
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { existsSync, mkdirSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, join, resolve } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '..', '..');
const outDir = join(here, 'out');
const pwDir = process.env.PLAYWRIGHT_DIR || resolve(root, '..', 'hacchu-forge', 'node_modules', 'playwright');

let chromium, browser, site, siteUrl, server, appUrl;
// robots.txt の内容。null なら 404(= 規則なし)。テストが差し替える
let robotsBody = null;

// 合成サイト: / → /p1..p24、各ページから / と隣へ。/p7 は 404、/img.png は画像。
function startSite() {
  return new Promise((ok) => {
    const n = 24;
    site = createServer((req, res) => {
      const delay = Number(new URL(req.url, 'http://x').searchParams.get('d') || 0);
      // 遅延はリンク先にも引き継ぐ。引き継がないと開始 URL だけが遅く、
      // 「Workers を増やしても総時間が変わらない」偽の観測になる(2026-09-07 に踏んだ)
      const q = delay ? `?d=${delay}` : '';
      const body = () => {
        if (req.url.startsWith('/sitemap.xml')) {
          // トップからは辿れない /hidden1, /hidden2 を挙げる(原本 §34 B の効きを測る)
          res.writeHead(200, { 'Content-Type': 'application/xml' });
          return res.end('<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">'
            + `<url><loc>${siteUrl}/hidden1</loc></url><url><loc>${siteUrl}/hidden2</loc></url></urlset>`);
        }
        if (req.url.startsWith('/robots.txt')) {
          if (robotsBody === null) {
            res.writeHead(404, { 'Content-Type': 'text/plain' });
            return res.end('no robots');
          }
          res.writeHead(200, { 'Content-Type': 'text/plain' });
          return res.end(robotsBody);
        }
        if (req.url.startsWith('/img.png')) {
          res.writeHead(200, { 'Content-Type': 'image/png' });
          return res.end('png');
        }
        if (req.url.startsWith('/p7')) {
          res.writeHead(404, { 'Content-Type': 'text/html' });
          return res.end('<title>nf</title>');
        }
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        if (req.url === '/' || req.url.startsWith('/?')) {
          let h = '<title>Synthetic root</title>';
          for (let i = 1; i <= n; i++) h += `<a href="/p${i}${q}">p${i}</a>`;
          h += `<a href="/img.png">img</a>`;
          // 外部ドメイン(原本 §34 C)。辿らずに数えられることを測る
          h += '<a href="https://other.example.org/">ext</a><a href="https://other.example.org/2">ext2</a>';
          h += '<a href="https://elsewhere.example.net/">ext3</a>';
          return res.end(h);
        }
        const m = /^\/p(\d+)/.exec(req.url);
        const i = m ? Number(m[1]) : 0;
        // 書誌情報(ROADMAP E)も返す。canonical はクエリを落とした形にして「自分と違う」場合を作る
        res.end(`<title>Page ${i}</title>`
          + `<meta name="description" content="ページ ${i} の説明">`
          + `<link rel="canonical" href="/p${i}">`
          + `<h1>見出し ${i}</h1>`
          + `<a href="/${q}">home</a><a href="/p${(i % n) + 1}${q}">next</a>`);
      };
      if (delay) setTimeout(body, delay); else body();
    });
    site.listen(0, '127.0.0.1', () => {
      siteUrl = `http://127.0.0.1:${site.address().port}`;
      ok();
    });
  });
}

function startServer() {
  return new Promise((ok, ng) => {
    const port = 3300 + Math.floor(Math.random() * 500);
    appUrl = `http://127.0.0.1:${port}`;
    // `go run .` は子プロセスにバイナリを起動するので kill が届かず、テストの終了が固まる(2026-09-06 実測)。
    // 先にビルドして、そのバイナリを直接 spawn する
    const goBin = process.env.GO_BIN || 'go';
    const bin = join(outDir, process.platform === 'win32' ? 'server.exe' : 'server');
    const build = spawnSync(goBin, ['build', '-o', bin, '.'], { cwd: root, encoding: 'utf8' });
    if (build.status !== 0) return ng(new Error(`go build failed: ${build.stderr}`));
    server = spawn(bin, [], {
      cwd: root,
      env: { ...process.env, PORT: String(port), GPWC_ALLOW_PRIVATE: '1' },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let logs = '';
    server.stderr.on('data', (d) => { logs += d; });
    server.on('exit', (code) => { if (code) ng(new Error(`server exited ${code}: ${logs}`)); });
    const t0 = Date.now();
    (async function poll() {
      try {
        const r = await fetch(`${appUrl}/api/health`);
        if (r.ok) return ok();
      } catch { /* まだ起動していない */ }
      if (Date.now() - t0 > 60000) return ng(new Error(`server did not start: ${logs}`));
      setTimeout(poll, 250);
    })();
  });
}

before(async () => {
  assert.ok(existsSync(pwDir), `playwright が見つからない: ${pwDir}`);
  ({ chromium } = await import(pathToFileURL(join(pwDir, 'index.mjs')).href));
  mkdirSync(outDir, { recursive: true });
  await startSite();
  await startServer();
  browser = await chromium.launch();
});

after(async () => {
  await browser?.close();
  server?.kill();
  site?.close();
});

async function runCrawl(page, { url, workers, maxPages, delay, sitemap = false }) {
  if (sitemap) await page.check('#sitemap'); else await page.uncheck('#sitemap');
  await page.fill('#url', url);
  await page.$eval('#workers', (el, v) => { el.value = v; el.dispatchEvent(new Event('input', { bubbles: true })); }, String(workers));
  await page.$eval('#maxPages', (el, v) => { el.value = v; el.dispatchEvent(new Event('input', { bubbles: true })); }, String(maxPages));
  await page.fill('#delay', String(delay));
  await page.click('#start');
}

// 図の全要素が viewBox に収まっているか(HC-159)。はみ出した要素の説明を返す。
async function svgOverflows(page) {
  return page.$eval('#graph', (svg) => {
    const vb = svg.viewBox.baseVal;
    const out = [];
    for (const el of svg.querySelectorAll('circle, line, text')) {
      if (el.tagName === 'text' && !el.textContent) continue;
      const b = el.getBBox();
      if (b.x < vb.x || b.y < vb.y || b.x + b.width > vb.x + vb.width || b.y + b.height > vb.y + vb.height) {
        out.push(`${el.tagName} ${el.textContent || ''} bbox=(${b.x.toFixed(1)},${b.y.toFixed(1)},${b.width.toFixed(1)},${b.height.toFixed(1)})`);
      }
    }
    return out;
  });
}

test('T-402: 合成サイトをクロールし、Worker 行・ノード・幾何・溢れを測る', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  await page.goto(`${appUrl}/`);
  await runCrawl(page, { url: `${siteUrl}/`, workers: 3, maxPages: 20, delay: 0 });
  await page.waitForFunction(() => document.getElementById('status').dataset.status === 'completed', null, { timeout: 30000 });
  assert.deepEqual(errors, [], 'ブラウザのエラー');

  // Worker 行 = workers、全員 ✓
  const workerRows = await page.$$eval('#workerList .worker', (els) => els.map((e) => [e.dataset.state, e.children[1].textContent]));
  assert.equal(workerRows.length, 3);
  for (const [st, sym] of workerRows) { assert.equal(st, 'completed'); assert.equal(sym, '✓'); }

  // 取得済みノード数 = ページ表の行数 = 統計 Pages(= maxPages: 到達可能 26 > 20)
  const pagesText = Number(await page.$eval('#stPages', (e) => e.textContent));
  // 詳細行(tr.detail)は数えない —— 1 ページにつき本体 1 行 + 任意で詳細 1 行
  const rows = await page.$$eval('#pages tr:not(.detail)', (els) => els.length);
  const fetchedNodes = await page.$$eval('#graph circle.node.fetched, #graph circle.node.error', (els) => els.length);
  const allNodes = await page.$$eval('#graph circle.node', (els) => els.length);
  assert.equal(pagesText, 20);
  assert.equal(rows, 20);
  assert.equal(fetchedNodes, 20);
  // 上限(20)で切られた先も link_found(queued=false)で届くので、ノードは取得済みより多い。
  // 到達可能は 26(/ + p1..p24 + /p7 は 404 だが取得対象)なので all は 21..26 の範囲
  assert.ok(allNodes > fetchedNodes && allNodes <= 26, `発見のみのノードが描かれている: all=${allNodes}`);
  // URLSet(既知の URL)= maxPages に張り付く。Links はサーバの links 数と同じ規則(一意な辺)
  assert.equal(Number(await page.$eval('#pipeDiscovered', (e) => e.textContent)), 20);
  const linksText = Number(await page.$eval('#stLinks', (e) => e.textContent));
  const edgeEls = await page.$$eval('#graph line.edge', (els) => els.length);
  assert.equal(edgeEls, linksText);

  // エラー 1 件(/p7 が 404)。img.png は拡張子で除外されるので取得されない
  const errCount = Number(await page.$eval('#errCount', (e) => e.textContent));
  assert.equal(errCount, 1);
  const errText = await page.$eval('#errors', (e) => e.textContent);
  assert.match(errText, /\/p7/);
  assert.match(errText, /HTTP 404/);
  // ROADMAP D: 壊れたページには参照元が付く(/p7 を指しているのは / と隣のページ)
  assert.match(errText, /参照元: .*\//);
  // 状態の記号が全行に付き、200 の行は ✓
  const syms = await page.$$eval('#pages tr:not(.detail) td.psym', (els) => els.map((e) => e.textContent));
  assert.equal(syms.length, 20);
  assert.equal(syms.filter((s) => s === '✓').length, 19);
  assert.equal(syms.filter((s) => s === '✕').length, 1);
  const pageUrls = await page.$$eval('#pages td.purl', (els) => els.map((e) => e.title));
  assert.ok(!pageUrls.some((u) => u.endsWith('/img.png')), 'img.png が取得されている');

  // 案内文は完了後に不可視(hidden 属性を CSS の display が上書きしていた欠陥の再発防止。目視で発見 2026-09-06)
  const emptyVisible = await page.$eval('#graphEmpty', (e) => getComputedStyle(e).display !== 'none');
  assert.equal(emptyVisible, false, '案内文がグラフに重なったまま');

  // 幾何: SVG の全要素が viewBox 内(HC-159)
  await page.waitForTimeout(1200); // レイアウトが落ち着くのを待つ
  assert.deepEqual(await svgOverflows(page), []);
  // 幾何の陽性対照: わざと外に置いた text を検査が捕まえる(HC-080)
  await page.$eval('#graph', (svg) => {
    const t = document.createElementNS('http://www.w3.org/2000/svg', 'text');
    t.setAttribute('x', '700'); t.setAttribute('y', '10'); t.textContent = 'OUTSIDE'; t.id = 'probe';
    svg.appendChild(t);
  });
  const caught = await svgOverflows(page);
  assert.equal(caught.length, 1, `陽性対照が捕まらない: ${JSON.stringify(caught)}`);
  await page.$eval('#probe', (t) => t.remove());

  // 横溢れ・縦の伸びすぎ(HC-078)
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await page.waitForTimeout(200);
    const m = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: window.innerWidth, sh: document.documentElement.scrollHeight }));
    assert.ok(m.sw <= m.iw, `width=${width}: 横溢れ scrollWidth=${m.sw} > innerWidth=${m.iw}`);
    assert.ok(m.sh < 16000, `width=${width}: 縦が伸びすぎ ${m.sh}`);
    await page.screenshot({ path: join(outDir, `crawl-${width}.png`), fullPage: true });
  }
  await page.close();
});

test('T-402b: STOP で cancelled になり、Worker が全員 completed で畳まれる', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  await page.goto(`${appUrl}/`);
  await runCrawl(page, { url: `${siteUrl}/?d=250`, workers: 2, maxPages: 100, delay: 300 });
  await page.waitForFunction(() => Number(document.getElementById('stPages').textContent) >= 2, null, { timeout: 20000 });
  await page.click('#stop');
  await page.waitForFunction(() => document.getElementById('status').dataset.status === 'completed', null, { timeout: 15000 });
  const reason = await page.$eval('#reason', (e) => e.textContent);
  assert.match(reason, /cancelled/);
  const pages = Number(await page.$eval('#stPages', (e) => e.textContent));
  assert.ok(pages < 26, `止まっていない: pages=${pages}`);
  const states = await page.$$eval('#workerList .worker', (els) => els.map((e) => e.dataset.state));
  assert.deepEqual(states, ['completed', 'completed']);
  await page.screenshot({ path: join(outDir, 'stopped.png'), fullPage: true });
  await page.close();
});

// T-602 / F-38: BENCHMARK が Workers を変えて 4 回走り、表が埋まり、速度比が出る。
test('T-602: Worker Benchmark が 4 行そろい、最速行に印が付く', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('dialog', (d) => d.accept()); // 実行前の確認
  await page.goto(`${appUrl}/`);
  // 各応答を 300 ms 遅らせる。所要はおよそ ceil(pages/workers) × delay なので、
  // 12 ページ・最大 Workers=10 でも 2 × 300 = 600 ms となり、全行が MIN_BENCH_MS(500 ms)を超える。
  // 遅延なしだと 20〜50 ms で終わり、差は往復のばらつきになる(T-602c で別に検査)
  await page.fill('#url', `${siteUrl}/?d=300`);
  await page.$eval('#maxPages', (el) => { el.value = '12'; el.dispatchEvent(new Event('input', { bubbles: true })); });
  await page.fill('#delay', '0');
  // 走らせる前は隠れている(hidden が効いていること — HC-190)
  assert.equal(await page.$eval('#benchPanel', (e) => getComputedStyle(e).display), 'none');
  await page.click('#bench');
  await page.waitForFunction(() => {
    const rows = [...document.querySelectorAll('#benchBody tr')];
    return rows.length === 4 && rows.every((r) => !r.classList.contains('pending'));
  }, null, { timeout: 60000 });
  assert.deepEqual(errors, []);

  const rows = await page.$$eval('#benchBody tr', (els) => els.map((r) => ({
    cells: [...r.children].map((c) => c.textContent.trim()),
    best: r.classList.contains('best'),
    barPct: r.querySelector('.bar') ? parseFloat(r.querySelector('.bar').style.width) : null,
  })));
  assert.deepEqual(rows.map((r) => r.cells[0]), ['1', '2', '5', '10']);
  // 前提の検算: 全行が下限(MIN_BENCH_MS = 500ms)を超えている。超えていなければ
  // 速度比は出ない仕様なので、以下の期待は成立しえない(HC-070: 対照の前提を assert で固定する)
  const secs = rows.map((r) => parseFloat(r.cells[1]));
  assert.ok(secs.every((s) => s >= 0.5), `前提が崩れている(所要 ${secs.join('/')}s)`);
  // 全行が同じページ数を取れている(maxPages=12・到達可能 26 なので必ず 12)。
  // 合成サイトの /p7 は 404 なので、12 件のうちエラーが混じる行がある(表記は「12 (err 1)」)
  assert.deepEqual(rows.map((r) => parseInt(r.cells[5], 10)), [12, 12, 12, 12]);
  assert.equal(rows.filter((r) => r.best).length, 1, '最速行の印が 1 行だけ付く');
  // 棒は 0..100% に収まり、最速行が 100%
  for (const r of rows) assert.ok(r.barPct > 0 && r.barPct <= 100, `bar=${r.barPct}`);
  assert.equal(rows.find((r) => r.best).barPct, 100);
  // 速度比が出ている(comparable=true なので「—」ではない)
  for (const r of rows) assert.match(r.cells[4], /^×[\d.]+$/);
  const verdict = await page.$eval('#benchVerdict', (e) => e.textContent);
  assert.match(verdict, /倍/);
  assert.match(verdict, /一般的な性能とは読まないでください/);
  // 溢れが出ていない
  const m = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: window.innerWidth }));
  assert.ok(m.sw <= m.iw, `横溢れ ${m.sw} > ${m.iw}`);
  await page.screenshot({ path: join(outDir, 'benchmark.png'), fullPage: true });
  await page.close();
});

// T-602c / G-13: 速すぎるクロールでは速度比を出さず、理由と手当てを書く(裏づけの無い数を出さない)。
test('T-602c: 短すぎる計測では速度比も最速の印も出さない', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  page.on('dialog', (d) => d.accept());
  await page.goto(`${appUrl}/`);
  await page.fill('#url', `${siteUrl}/`); // 遅延なし = 数十 ms で終わる
  await page.$eval('#maxPages', (el) => { el.value = '12'; el.dispatchEvent(new Event('input', { bubbles: true })); });
  await page.fill('#delay', '0');
  await page.click('#bench');
  await page.waitForFunction(() => {
    const rows = [...document.querySelectorAll('#benchBody tr')];
    return rows.length === 4 && rows.every((r) => !r.classList.contains('pending'));
  }, null, { timeout: 60000 });
  // 前提の検算: 実際に短かった(各行 500ms 未満)
  const secs = await page.$$eval('#benchBody tr', (els) => els.map((r) => parseFloat(r.children[1].textContent)));
  assert.ok(secs.every((s) => s < 0.5), `前提が崩れている(所要 ${secs.join('/')}s)`);
  const ratios = await page.$$eval('#benchBody tr', (els) => els.map((r) => r.children[4].textContent.trim()));
  assert.deepEqual(ratios, ['—', '—', '—', '—'], '短すぎる計測で速度比が出ている');
  assert.equal(await page.$$eval('#benchBody tr.best', (els) => els.length), 0, '最速の印が出ている');
  const verdict = await page.$eval('#benchVerdict', (e) => e.textContent);
  assert.match(verdict, /往復のばらつき/);
  assert.match(verdict, /Max Pages を増やす/);
  await page.close();
});

test('T-602b: 確認をキャンセルするとベンチは走らない', async () => {
  const page = await browser.newPage();
  page.on('dialog', (d) => d.dismiss());
  await page.goto(`${appUrl}/`);
  await page.fill('#url', `${siteUrl}/`);
  await page.click('#bench');
  await page.waitForTimeout(600);
  assert.equal(await page.$eval('#benchPanel', (e) => getComputedStyle(e).display), 'none');
  assert.equal(await page.$eval('#status', (e) => e.dataset.status), 'idle');
  await page.close();
});

// T-907 / ROADMAP-E: ページ行を押すと description / h1 / canonical が開く。
test('T-907: 書誌情報の行が押すと開き、canonical の異同まで出る', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  await page.goto(`${appUrl}/`);
  await runCrawl(page, { url: `${siteUrl}/`, workers: 2, maxPages: 6, delay: 0 });
  await page.waitForFunction(() => document.getElementById('status').dataset.status === 'completed', null, { timeout: 30000 });

  // 詳細行は初期状態で閉じている(hidden が効いていること — HC-193)
  const hiddenAtRest = await page.$$eval('#pages tr.detail', (els) => els.map((e) => getComputedStyle(e).display));
  assert.ok(hiddenAtRest.length > 0, '詳細行が 1 つも作られていない');
  assert.ok(hiddenAtRest.every((d) => d === 'none'), '詳細行が最初から開いている');

  // /p 系の行を押す(開始 URL の行には h1 が無いので、2 行目以降を選ぶ)
  const target = await page.evaluateHandle(() => {
    const rows = [...document.querySelectorAll('#pages tr.has-detail')];
    return rows.find((r) => r.querySelector('td.purl').title.includes('/p')) || rows[0];
  });
  await target.asElement().scrollIntoViewIfNeeded();
  await target.asElement().click();
  await page.waitForFunction(() => [...document.querySelectorAll('#pages tr.detail')]
    .some((e) => getComputedStyle(e).display !== 'none'), null, { timeout: 5000 });

  const open = await page.$$eval('#pages tr.detail', (els) => els
    .filter((e) => getComputedStyle(e).display !== 'none')
    .map((e) => e.textContent));
  assert.equal(open.length, 1, '押した行の詳細だけが開く');
  assert.match(open[0], /見出し/);
  assert.match(open[0], /の説明/);
  assert.match(open[0], /canonical/);
  await page.close();
});

// T-1115 / ROADMAP-C: 外部ドメインを辿らずに数え、表に出す。
test('T-1115: 外部ドメインの表が出て、辿ってはいない', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await page.goto(`${appUrl}/`);
  // 走らせる前は隠れている(hidden が効いていること — HC-193)
  assert.equal(await page.$eval('#externalPanel', (e) => getComputedStyle(e).display), 'none');
  await runCrawl(page, { url: `${siteUrl}/`, workers: 3, maxPages: 10, delay: 0 });
  await page.waitForFunction(() => document.getElementById('status').dataset.status === 'completed', null, { timeout: 30000 });
  assert.deepEqual(errors, []);

  const rows = await page.$$eval('#externalBody tr', (els) => els.map((r) => [...r.children].map((c) => c.textContent.trim())));
  const hosts = rows.map((r) => r[0]);
  assert.ok(hosts.includes('other.example.org'), `外部ドメインが無い: ${JSON.stringify(hosts)}`);
  assert.ok(hosts.includes('elsewhere.example.net'), `2 つ目の外部ドメインが無い: ${JSON.stringify(hosts)}`);
  // other.example.org は 2 つの URL を持つので elsewhere より上に来る(件数の降順)
  assert.equal(hosts[0], 'other.example.org');
  assert.equal(rows[0][1], '2');
  // 統計にも総数が出ている
  assert.ok(Number(await page.$eval('#stExternal', (e) => e.textContent)) >= 3);
  // **辿っていないこと**: ページ一覧に外部ドメインが 1 件も無い
  const urls = await page.$$eval('#pages td.purl', (els) => els.map((e) => e.title));
  assert.ok(!urls.some((u) => u.includes('example.org') || u.includes('example.net')), '外部ドメインを取得した');
  await page.screenshot({ path: join(outDir, 'external.png'), fullPage: true });
  await page.close();
});

// T-1116 / ROADMAP-B: サイトマップを使うと、トップから辿れないページに届く。
// **対照**: チェックを外すと届かない。
test('T-1116: sitemap.xml を種にすると隠れたページに届く', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  await page.goto(`${appUrl}/`);

  // 対照(使わない): /hidden1 に届かない
  await runCrawl(page, { url: `${siteUrl}/`, workers: 3, maxPages: 30, delay: 0, sitemap: false });
  await page.waitForFunction(() => document.getElementById('status').dataset.status === 'completed', null, { timeout: 30000 });
  const without = await page.$$eval('#pages td.purl', (els) => els.map((e) => e.title));
  assert.ok(!without.some((u) => u.includes('/hidden')), '使わない設定でも隠れたページに届いた —— 対照が成立していない');
  assert.equal(await page.$eval('#sitemapStatus', (e) => e.textContent), '');

  // 使う: 届く
  await runCrawl(page, { url: `${siteUrl}/`, workers: 3, maxPages: 30, delay: 0, sitemap: true });
  await page.waitForFunction(() => document.getElementById('status').dataset.status === 'completed', null, { timeout: 30000 });
  const withSm = await page.$$eval('#pages td.purl', (els) => els.map((e) => e.title));
  assert.ok(withSm.some((u) => u.endsWith('/hidden1')), `隠れたページに届いていない: ${withSm.length} ページ`);
  assert.ok(withSm.some((u) => u.endsWith('/hidden2')));
  const status = await page.$eval('#sitemapStatus', (e) => e.textContent);
  assert.match(status, /2 件の URL を読み/);
  await page.close();
});

// T-1011 / ROADMAP-G: 言語比較の節が実測ファイルから描かれ、成立しない行では倍率を出さない。
test('T-1011: Go vs TypeScript の表が出て、成立しない行は理由を書く', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await page.goto(`${appUrl}/`);
  await page.waitForFunction(() => {
    const el = document.getElementById('langPanel');
    return el && getComputedStyle(el).display !== 'none' && document.querySelectorAll('#langBody tr').length > 0;
  }, null, { timeout: 15000 });
  assert.deepEqual(errors, []);

  const rows = await page.$$eval('#langBody tr', (els) => els.map((r) => ({
    cells: [...r.children].map((c) => c.textContent.trim()),
    pending: r.classList.contains('pending'),
  })));
  assert.ok(rows.length >= 2, `行が少ない: ${rows.length}`);
  for (const r of rows) {
    if (r.pending) {
      assert.equal(r.cells[3], '—', `成立しない行に倍率が出ている: ${r.cells[0]}`);
      assert.ok(r.cells[4].length > 3, `理由が空: ${r.cells[0]}`);
    } else {
      assert.match(r.cells[3], /^×[\d.]+$/, `倍率の形が違う: ${r.cells[3]}`);
    }
  }
  // 前提の検算: 成立した行が少なくとも 1 つある(全滅なら表そのものが無意味)
  assert.ok(rows.some((r) => !r.pending), '成立した行が無い');
  // 注意書きと条件が必ず出ている
  const panel = await page.$eval('#langPanel', (e) => e.textContent);
  assert.match(panel, /言語の一般的な速さとして読まないでください/);
  assert.match(panel, /測定条件:/);
  await page.screenshot({ path: join(outDir, 'lang.png'), fullPage: true });
  await page.close();
});

// T-726 / ROADMAP-A: 画面が robots.txt に既定で従い、状態と除外件数を出す。
test('T-726: robots.txt に従い、状態と除外件数を画面に出す', async () => {
  robotsBody = 'User-agent: *\nDisallow: /p1\nAllow: /p10\n'; // /p1, /p11..p19 を拒否・/p10 は許可
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = [];
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${appUrl}/`);
    // 既定は「従う」
    assert.equal(await page.$eval('#robots', (e) => e.checked), true);
    await runCrawl(page, { url: `${siteUrl}/`, workers: 3, maxPages: 20, delay: 0 });
    await page.waitForFunction(() => document.getElementById('status').dataset.status === 'completed', null, { timeout: 30000 });
    assert.deepEqual(errors, []);

    const status = await page.$eval('#robotsStatus', (e) => e.textContent);
    assert.match(status, /取得して従っています/);
    const blocked = Number(await page.$eval('#stRobots', (e) => e.textContent));
    assert.ok(blocked > 0, 'robots 除外が 0 —— 規則が効いていない');
    // 前提の検算: /p1 と /p11 は取得されず、Allow の /p10 は取得されている
    const urls = await page.$$eval('#pages td.purl', (els) => els.map((e) => e.title));
    const paths = urls.map((u) => new URL(u).pathname);
    assert.ok(!paths.includes('/p1'), 'Disallow の /p1 を取得した');
    assert.ok(!paths.includes('/p11'), 'Disallow の前方一致 /p11 を取得した');
    assert.ok(paths.includes('/p10'), 'Allow の /p10 が取得されていない(最長一致が効いていない)');
    await page.close();
  } finally {
    robotsBody = null;
  }
});

// T-727: 「従わない」に外すと、拒否されたページも取る(自分のサイト向けの逃げ道)。
test('T-727: robots.txt を外すと拒否されたページも取る', async () => {
  robotsBody = 'User-agent: *\nDisallow: /\n';
  try {
    const page = await browser.newPage();
    await page.goto(`${appUrl}/`);
    await page.uncheck('#robots');
    await runCrawl(page, { url: `${siteUrl}/`, workers: 2, maxPages: 5, delay: 0 });
    await page.waitForFunction(() => document.getElementById('status').dataset.status === 'completed', null, { timeout: 30000 });
    const pages = Number(await page.$eval('#stPages', (e) => e.textContent));
    assert.ok(pages > 0, 'Disallow: / に従ってしまっている');
    const status = await page.$eval('#robotsStatus', (e) => e.textContent);
    assert.match(status, /従わない設定/);
    await page.close();
  } finally {
    robotsBody = null;
  }
});

// T-728: Disallow: / のサイトを既定で叩くと、1 ページも取らずに理由を書いて終わる。
test('T-728: Disallow: / のサイトはクロールせず理由を出す', async () => {
  robotsBody = 'User-agent: *\nDisallow: /\n';
  try {
    const page = await browser.newPage();
    await page.goto(`${appUrl}/`);
    await runCrawl(page, { url: `${siteUrl}/`, workers: 2, maxPages: 5, delay: 0 });
    await page.waitForFunction(() => document.getElementById('status').dataset.status === 'completed', null, { timeout: 30000 });
    assert.equal(Number(await page.$eval('#stPages', (e) => e.textContent)), 0);
    const reason = await page.$eval('#reason', (e) => e.textContent);
    assert.match(reason, /robots.txt がこのクロールを許していない/);
    await page.close();
  } finally {
    robotsBody = null;
  }
});

test('T-403: フッタ 5 項目の並び(DOM で検査)', async () => {
  const page = await browser.newPage();
  await page.goto(`${appUrl}/`);
  const items = await page.$$eval('footer.fleet-footer a', (as) => as.map((a) => a.textContent.trim()));
  assert.equal(items.length, 5);
  assert.equal(items[0], 'MIT License');
  assert.equal(items[1], 'GitHub');
  assert.equal(items[4], 'App Menu');
  const text = await page.$eval('footer.fleet-footer', (f) => f.innerText.replace(/\s+/g, ' '));
  assert.match(text, /MIT License © 2026 坂田哲朗 ・ GitHub ・ .+ ・ .+ ・ App Menu/);
  const pos = await page.$eval('footer.fleet-footer', (f) => getComputedStyle(f).position);
  assert.equal(pos, 'fixed');
  await page.close();
});

test('T-402c: 内部 URL は 400 で拒否され、画面にエラーが出る(サーバ側の SSRF はテストでは無効化されているので境界エラーで代替)', async () => {
  const page = await browser.newPage();
  await page.goto(`${appUrl}/`);
  await page.fill('#url', 'ftp://example.com/');
  await page.evaluate(() => document.getElementById('url').setCustomValidity(''));
  // type=url は ftp: を通すので、サーバ側の scheme 検査に届く
  await page.$eval('#form', (f) => f.requestSubmit());
  await page.waitForFunction(() => document.getElementById('status').dataset.status === 'error', null, { timeout: 10000 });
  const msg = await page.$eval('#message', (e) => e.textContent);
  assert.match(msg, /scheme/);
  await page.close();
});
