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

// 合成サイト: / → /p1..p24、各ページから / と隣へ。/p7 は 404、/img.png は画像。
function startSite() {
  return new Promise((ok) => {
    const n = 24;
    site = createServer((req, res) => {
      const delay = Number(new URL(req.url, 'http://x').searchParams.get('d') || 0);
      const body = () => {
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
          for (let i = 1; i <= n; i++) h += `<a href="/p${i}">p${i}</a>`;
          h += '<a href="/img.png">img</a><a href="https://other.example.org/">ext</a>';
          return res.end(h);
        }
        const m = /^\/p(\d+)/.exec(req.url);
        const i = m ? Number(m[1]) : 0;
        res.end(`<title>Page ${i}</title><a href="/">home</a><a href="/p${(i % n) + 1}">next</a>`);
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

async function runCrawl(page, { url, workers, maxPages, delay }) {
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
  const rows = await page.$$eval('#pages tr', (els) => els.length);
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
