// app.js — DOM と通信。状態の更新は state.js の reduce に任せ、ここは描画と fetch だけを持つ。
import {
  createSSEParser, initialState, reduce, liveStats, channelCounts, STATUS, WORKER,
  BENCH_WORKERS, benchRow, benchSummary, MIN_BENCH_MS,
} from './state.js';

const $ = (id) => document.getElementById(id);
const els = {
  form: $('form'), url: $('url'), workers: $('workers'), workersOut: $('workersOut'),
  maxPages: $('maxPages'), maxPagesOut: $('maxPagesOut'), delay: $('delay'),
  start: $('start'), stop: $('stop'), status: $('status'), message: $('message'),
  pipeQueued: $('pipeQueued'), pipeCrawling: $('pipeCrawling'), pipeWorkers: $('pipeWorkers'),
  pipeDone: $('pipeDone'), pipeDiscovered: $('pipeDiscovered'),
  graph: $('graph'), graphEmpty: $('graphEmpty'), workerList: $('workerList'),
  stPages: $('stPages'), stLinks: $('stLinks'), stSuccess: $('stSuccess'), stErrors: $('stErrors'),
  stElapsed: $('stElapsed'), stRps: $('stRps'), stAvg: $('stAvg'), stP95: $('stP95'), reason: $('reason'),
  errors: $('errors'), errCount: $('errCount'), pages: $('pages'),
  bench: $('bench'), benchPanel: $('benchPanel'), benchBody: $('benchBody'), benchNote: $('benchNote'), benchVerdict: $('benchVerdict'),
};

let state = initialState();
let controller = null; // AbortController(STOP の第二経路)
let startedAtLocal = 0;
let uiStatus = STATUS.IDLE; // stopping は画面側だけの状態
// ベンチマーク(F-38)の進行状況。setStatus が読むので、他の状態と一緒にここで宣言する
let benchRunning = false;
let benchAt = 0;
let benchRows = [];
let raf = 0;
let dirty = true;

// ---------- 入力 ----------
els.workers.addEventListener('input', () => { els.workersOut.value = els.workers.value; });
els.maxPages.addEventListener('input', () => { els.maxPagesOut.value = els.maxPages.value; });

els.form.addEventListener('submit', (ev) => {
  ev.preventDefault();
  startCrawl();
});
els.stop.addEventListener('click', () => stopCrawl());

function setStatus(s) {
  uiStatus = s;
  els.status.dataset.status = s;
  els.status.textContent = benchRunning ? `benchmark ${benchAt}/${BENCH_WORKERS.length}` : s;
  const running = s === STATUS.RUNNING || s === STATUS.STOPPING;
  els.start.disabled = running;
  els.stop.disabled = s !== STATUS.RUNNING;
  els.bench.disabled = running;
  for (const el of [els.url, els.workers, els.maxPages, els.delay]) el.disabled = running;
}

function showMessage(text) {
  els.message.textContent = text || '';
  els.message.hidden = !text;
}

// ---------- 通信 ----------
async function startCrawl(override) {
  showMessage('');
  const body = {
    url: els.url.value.trim(),
    workers: Number(els.workers.value),
    maxPages: Number(els.maxPages.value),
    requestDelayMs: Number(els.delay.value),
    ...override,
  };
  state = initialState();
  graph.reset();
  startedAtLocal = performance.now();
  setStatus(STATUS.RUNNING);
  render();
  controller = new AbortController();
  const parser = createSSEParser(onEvent);
  try {
    const res = await fetch('/api/crawl', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
      body: JSON.stringify(body),
      signal: controller.signal,
    });
    if (!res.ok) {
      let msg = `HTTP ${res.status}`;
      try { msg = (await res.json()).error || msg; } catch { /* 本文が JSON でない */ }
      throw new Error(msg);
    }
    // POST なので EventSource は使えない。ReadableStream を読んで自前で SSE に分ける(F-36)。
    // サーバ側で Flush が効かない環境では応答の終わりに一括で届くが、パーサは同じ結果を出す(G-09)。
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      parser.push(decoder.decode(value, { stream: true }));
    }
    parser.end();
    if (state.status !== STATUS.COMPLETED) {
      // crawl_completed が来ずに閉じた(サーバ側の上限時間・切断)
      state.status = STATUS.COMPLETED;
      state.reason = state.reason || 'closed';
    }
    setStatus(STATUS.COMPLETED);
  } catch (err) {
    if (err && err.name === 'AbortError') {
      // 第二経路(abort)で止めた。サーバは r.Context() の cancel で畳まれる
      state.status = STATUS.COMPLETED;
      state.reason = state.reason || 'cancelled';
      setStatus(STATUS.COMPLETED);
    } else {
      setStatus(STATUS.ERROR);
      showMessage(String(err && err.message ? err.message : err));
    }
  } finally {
    controller = null;
    render();
  }
  return { stats: state.stats, reason: state.reason, ok: uiStatus === STATUS.COMPLETED };
}

function onEvent(e) {
  state = reduce(state, e);
  if (e.type === 'link_found' || e.type === 'crawl_started') graph.sync(state);
  if (e.type === 'page_completed' || e.type === 'worker_started') graph.sync(state);
  dirty = true;
}

async function stopCrawl() {
  if (uiStatus !== STATUS.RUNNING) return;
  setStatus(STATUS.STOPPING);
  // 第一経路: /stop(同じインスタンスに当たれば context が cancel され、crawl_completed(cancelled)が届く)
  let acked = false;
  if (state.crawlId) {
    try {
      const r = await fetch(`/api/crawl/${encodeURIComponent(state.crawlId)}/stop`, { method: 'POST' });
      acked = r.ok;
    } catch { acked = false; }
  }
  // 第二経路: 1.5 秒待っても完了が来なければ接続を切る(サーバレスで別インスタンスに当たった場合)
  const deadline = performance.now() + (acked ? 1500 : 0);
  while (state.status !== STATUS.COMPLETED && performance.now() < deadline) {
    await new Promise((r) => setTimeout(r, 50));
  }
  if (state.status !== STATUS.COMPLETED && controller) controller.abort();
}

// ---------- ベンチマーク(F-38)----------
els.bench.addEventListener('click', () => runBenchmark());

async function runBenchmark() {
  if (benchRunning || uiStatus === STATUS.RUNNING) return;
  const url = els.url.value.trim();
  if (!url) { showMessage('Target URL を入れてください'); return; }
  // 対象サイトへのアクセスは BENCH_WORKERS の本数だけ繰り返される。上限を絞ってから確認を取る
  const maxPages = Math.min(Number(els.maxPages.value), 30);
  const times = BENCH_WORKERS.length;
  const ok = window.confirm(
    `${url} を Workers=${BENCH_WORKERS.join(' / ')} で ${times} 回クロールします。`
    + `\n1 回あたり最大 ${maxPages} ページなので、対象サイトへのアクセスは最大 ${maxPages * times} 回です。`
    + '\n自分が管理するサイト、またはクロールが許可されているサイトですか?');
  if (!ok) return;

  benchRunning = true;
  benchRows = [];
  benchAt = 0;
  els.benchPanel.hidden = false;
  renderBench();
  for (const workers of BENCH_WORKERS) {
    benchAt += 1;
    renderBench();
    const res = await startCrawl({ workers, maxPages });
    if (!res.ok || !res.stats) {
      // 途中で失敗・中断したらそこで止める(半端な行を比較に混ぜない)
      break;
    }
    benchRows.push(benchRow(workers, res.stats, res.reason));
    renderBench();
  }
  benchRunning = false;
  benchAt = 0;
  setStatus(uiStatus);
  renderBench();
}

function renderBench() {
  const s = benchSummary(benchRows);
  els.benchBody.textContent = '';
  const done = new Set(benchRows.map((r) => r.workers));
  BENCH_WORKERS.forEach((w, i) => {
    const row = benchRows.find((r) => r.workers === w);
    const tr = document.createElement('tr');
    if (!row) {
      tr.className = 'pending';
      const running = benchRunning && benchAt === i + 1;
      const cells = [`${w}`, running ? '計測中…' : (done.size ? '—' : '待機'), '—', '', '—', '—', '—'];
      for (const text of cells) {
        const td = document.createElement('td');
        td.textContent = text;
        tr.appendChild(td);
      }
      els.benchBody.appendChild(tr);
      return;
    }
    if (s.trustworthy && row.workers === s.fastest.workers) tr.className = 'best';
    const tds = [
      `${row.workers}`,
      `${row.seconds.toFixed(2)}s`,
      `${row.pagesPerSec}`,
      null, // 棒
      s.trustworthy ? `×${s.speedup(row)}` : '—',
      `${row.pages}${row.errors ? ` (err ${row.errors})` : ''}`,
      `${row.p95Ms}ms`,
    ];
    for (const text of tds) {
      const td = document.createElement('td');
      if (text === null) {
        td.className = 'barcell';
        const bar = document.createElement('span');
        bar.className = 'bar';
        bar.style.width = `${Math.max(2, s.bar(row) * 100)}%`;
        td.appendChild(bar);
      } else {
        td.textContent = text;
      }
      tr.appendChild(td);
    }
    els.benchBody.appendChild(tr);
  });

  if (benchRows.length === 0) {
    els.benchVerdict.textContent = benchRunning ? '' : '結果はまだありません。';
    return;
  }
  if (!s.comparable) {
    els.benchVerdict.textContent = benchRows.length < 2
      ? '比較には 2 行以上が要ります。'
      : `取得ページ数が行ごとに違う(${benchRows.map((r) => r.pages).join(' / ')})ので、速度比は出しません。`
        + ' 対象サイトが小さいか、途中で止まった可能性があります。';
    return;
  }
  if (!s.longEnough) {
    // 短すぎる計測は並行度でなく往復のばらつきを測ってしまう。数を出さずに手当てを書く
    const slowest = Math.max(...benchRows.map((r) => r.durationMs));
    els.benchVerdict.textContent =
      `どの回も ${slowest}ms 以内に終わっており(${MIN_BENCH_MS}ms 未満)、差は並行度ではなく往復のばらつきです。`
      + ' 速度比は出しません。Max Pages を増やすか、Request Delay を足すか、応答の遅いページを含む URL で試してください。';
    return;
  }
  const base = s.baseline, fast = s.fastest;
  els.benchVerdict.textContent =
    `Workers ${base.workers} → ${fast.workers} で ${s.speedup(fast)} 倍(${base.pagesPerSec} → ${fast.pagesPerSec} pages/s)。`
    + ' この数はネットワークと対象サイトの応答に左右されるので、言語や実装の一般的な性能とは読まないでください。';
}

// ---------- 描画 ----------
function fmtMs(ms) { return `${Math.round(ms)}ms`; }
function fmtSec(ms) { return `${(ms / 1000).toFixed(2)}s`; }
function pathOf(url) {
  try {
    const u = new URL(url);
    const p = u.pathname + u.search;
    return p || '/';
  } catch { return url; }
}
function hostOf(url) {
  try { return new URL(url).host; } catch { return ''; }
}

function render() {
  dirty = false;
  const running = uiStatus === STATUS.RUNNING || uiStatus === STATUS.STOPPING;
  const st = liveStats(state);
  const elapsed = running ? Math.max(st.durationMs, performance.now() - startedAtLocal) : st.durationMs;
  const rps = running && elapsed > 0 ? Math.round((st.total / (elapsed / 1000)) * 10) / 10 : st.requestsPerSec;

  const ch = channelCounts(state);
  els.pipeQueued.textContent = ch.queued;
  els.pipeCrawling.textContent = ch.crawling;
  els.pipeWorkers.textContent = state.config.workers;
  els.pipeDone.textContent = ch.done;
  els.pipeDiscovered.textContent = ch.discovered;

  els.stPages.textContent = st.total;
  els.stLinks.textContent = state.links.length;
  els.stSuccess.textContent = st.success;
  els.stErrors.textContent = st.errors;
  els.stElapsed.textContent = fmtSec(elapsed);
  els.stRps.textContent = rps;
  els.stAvg.textContent = fmtMs(st.avgMs);
  els.stP95.textContent = fmtMs(st.p95Ms);
  els.reason.textContent = state.reason ? `完了理由: ${reasonText(state.reason)}` : '';

  renderWorkers();
  renderPages();
  els.graphEmpty.hidden = state.nodes.size > 0;
}

function reasonText(r) {
  return {
    exhausted: 'exhausted — 辿れる URL が尽きた',
    max_pages: 'max_pages — 上限に達した(まだ未取得の URL がある)',
    cancelled: 'cancelled — STOP / 切断で context が cancel された',
    deadline: 'deadline — クロール全体の上限時間(60 秒)',
    closed: 'closed — 完了イベントを受け取る前に接続が閉じた',
  }[r] || r;
}

const SYM = { waiting: '○', crawling: '●', error: '!', completed: '✓' };
const LABEL = { waiting: 'Waiting', crawling: 'Crawling', error: 'Error', completed: 'Completed' };
function renderWorkers() {
  const ids = Object.keys(state.workers).map(Number).sort((a, b) => a - b);
  while (els.workerList.children.length > ids.length) els.workerList.lastChild.remove();
  ids.forEach((id, i) => {
    const w = state.workers[id];
    let li = els.workerList.children[i];
    if (!li) {
      li = document.createElement('li');
      li.className = 'worker';
      li.innerHTML = '<span class="wid"></span><span class="sym"></span><span class="wurl"></span><span class="wpages"></span>';
      els.workerList.appendChild(li);
    }
    li.dataset.state = w.status;
    li.dataset.workerId = String(id);
    li.children[0].textContent = `W${String(id).padStart(2, '0')}`;
    li.children[1].textContent = SYM[w.status] || '?';
    li.children[1].title = LABEL[w.status] || w.status;
    let text = LABEL[w.status];
    if (w.status === WORKER.CRAWLING) text = pathOf(w.url);
    else if (w.status === WORKER.ERROR) text = `${w.lastError} ${pathOf(w.url)}`;
    li.children[2].textContent = text;
    li.children[2].title = w.url || LABEL[w.status];
    li.children[3].textContent = `${w.pages}p`;
  });
}

let renderedPages = 0;
function renderPages() {
  if (state.pages.length < renderedPages) {
    els.pages.textContent = '';
    els.errors.textContent = '';
    renderedPages = 0;
  }
  for (let i = renderedPages; i < state.pages.length; i++) {
    const p = state.pages[i];
    const tr = document.createElement('tr');
    if (p.error) tr.className = 'err';
    const cells = [
      ['num', String(i + 1)],
      ['purl', pathOf(p.url)],
      ['pstatus', p.statusCode ? String(p.statusCode) : '—'],
      ['num ptime', fmtMs(p.durationMs)],
      ['num', `W${String(p.workerId).padStart(2, '0')}`],
      ['ptitle', p.error || p.title],
    ];
    for (const [cls, text] of cells) {
      const td = document.createElement('td');
      td.className = cls;
      td.textContent = text;
      if (cls === 'purl') td.title = p.url;
      tr.appendChild(td);
    }
    els.pages.appendChild(tr);
    if (p.error) {
      const li = document.createElement('li');
      li.innerHTML = '<span class="eurl"></span><span class="ekind"></span>';
      li.children[0].textContent = pathOf(p.url);
      li.children[0].title = p.url;
      li.children[1].textContent = p.error;
      els.errors.appendChild(li);
    }
  }
  renderedPages = state.pages.length;
  els.errCount.textContent = String(state.pages.filter((p) => p.error).length);
}

// ---------- グラフ(自前の力学レイアウト・SVG)----------
const graph = (() => {
  const W = 640, H = 480, PAD = 26, R = 6, LABEL_MAX = 18;
  const NS = 'http://www.w3.org/2000/svg';
  const gEdges = document.createElementNS(NS, 'g');
  const gNodes = document.createElementNS(NS, 'g');
  const gLabels = document.createElementNS(NS, 'g');
  els.graph.append(gEdges, gNodes, gLabels);
  const nodes = new Map(); // url → {x,y,vx,vy,el,label}
  const edges = new Map(); // "from to" → {a,b,el}
  let alpha = 0;

  function reset() {
    nodes.clear();
    edges.clear();
    gEdges.textContent = '';
    gNodes.textContent = '';
    gLabels.textContent = '';
    alpha = 0;
  }

  function place(url, parentUrl) {
    const parent = parentUrl ? nodes.get(parentUrl) : null;
    const angle = Math.random() * Math.PI * 2;
    const dist = 30 + Math.random() * 30;
    const x = parent ? parent.x + Math.cos(angle) * dist : W / 2;
    const y = parent ? parent.y + Math.sin(angle) * dist : H / 2;
    const el = document.createElementNS(NS, 'circle');
    el.setAttribute('r', String(parentUrl ? R : R + 3));
    el.classList.add('node');
    const title = document.createElementNS(NS, 'title');
    el.appendChild(title);
    gNodes.appendChild(el);
    const label = document.createElementNS(NS, 'text');
    label.classList.add('nlabel');
    label.setAttribute('text-anchor', 'middle');
    gLabels.appendChild(label);
    const n = { url, x, y, vx: 0, vy: 0, el, label, title, start: !parentUrl };
    nodes.set(url, n);
    return n;
  }

  function sync(state) {
    for (const [url, sn] of state.nodes) {
      let n = nodes.get(url);
      if (!n) n = place(url, sn.parent);
      let cls = 'pending';
      if (sn.fetched) cls = sn.error ? 'error' : 'fetched';
      const crawling = Object.values(state.workers).some((w) => w.status === WORKER.CRAWLING && w.url === url);
      if (crawling) cls = 'crawling';
      n.el.setAttribute('class', `node ${cls}${n.start ? ' start' : ''}`);
      n.title.textContent = `${url}${sn.error ? ` — ${sn.error}` : sn.statusCode ? ` — ${sn.statusCode}` : ''}`;
    }
    for (const l of state.links) {
      const key = `${l.from} ${l.to}`;
      if (edges.has(key)) continue;
      const a = nodes.get(l.from), b = nodes.get(l.to);
      if (!a || !b) continue;
      const el = document.createElementNS(NS, 'line');
      el.classList.add('edge');
      gEdges.appendChild(el);
      edges.set(key, { a, b, el });
    }
    alpha = 1;
    kick();
  }

  function step() {
    const list = [...nodes.values()];
    const k = 0.02; // ばね
    const rep = 1800; // 反発
    const len = 42; // 辺の自然長
    for (let i = 0; i < list.length; i++) {
      const a = list[i];
      for (let j = i + 1; j < list.length; j++) {
        const b = list[j];
        let dx = a.x - b.x, dy = a.y - b.y;
        let d2 = dx * dx + dy * dy;
        if (d2 < 1) { dx = Math.random() - 0.5; dy = Math.random() - 0.5; d2 = 1; }
        const f = rep / d2;
        const d = Math.sqrt(d2);
        const fx = (dx / d) * f, fy = (dy / d) * f;
        a.vx += fx; a.vy += fy; b.vx -= fx; b.vy -= fy;
      }
    }
    for (const e of edges.values()) {
      const dx = e.b.x - e.a.x, dy = e.b.y - e.a.y;
      const d = Math.sqrt(dx * dx + dy * dy) || 1;
      const f = (d - len) * k;
      const fx = (dx / d) * f, fy = (dy / d) * f;
      e.a.vx += fx; e.a.vy += fy; e.b.vx -= fx; e.b.vy -= fy;
    }
    for (const n of list) {
      n.vx += (W / 2 - n.x) * 0.002; // 中心へ
      n.vy += (H / 2 - n.y) * 0.002;
      n.vx *= 0.6; n.vy *= 0.6;
      n.x = Math.min(W - PAD, Math.max(PAD, n.x + n.vx * alpha));
      n.y = Math.min(H - PAD, Math.max(PAD, n.y + n.vy * alpha));
    }
    alpha = Math.max(0, alpha - 0.006);
  }

  function draw() {
    const showLabels = nodes.size <= 40;
    for (const n of nodes.values()) {
      n.el.setAttribute('cx', n.x.toFixed(1));
      n.el.setAttribute('cy', n.y.toFixed(1));
      if (showLabels || n.start) {
        let text = pathOf(n.url);
        if (n.start) text = hostOf(n.url) || text;
        if (text.length > LABEL_MAX) text = `${text.slice(0, LABEL_MAX - 1)}…`;
        // 文字幅の見積もり(9px 等幅 ≈ 5.5px/字)で viewBox からはみ出さない位置に寄せる(HC-159)
        const half = (text.length * 5.5) / 2 + 2;
        const x = Math.min(W - half, Math.max(half, n.x));
        const y = Math.min(H - 4, n.y + R + 11);
        n.label.textContent = text;
        n.label.setAttribute('x', x.toFixed(1));
        n.label.setAttribute('y', y.toFixed(1));
      } else {
        n.label.textContent = '';
      }
    }
    for (const e of edges.values()) {
      e.el.setAttribute('x1', e.a.x.toFixed(1));
      e.el.setAttribute('y1', e.a.y.toFixed(1));
      e.el.setAttribute('x2', e.b.x.toFixed(1));
      e.el.setAttribute('y2', e.b.y.toFixed(1));
    }
  }

  let ticking = false;
  function kick() {
    if (ticking) return;
    ticking = true;
    const loop = () => {
      if (alpha <= 0) { ticking = false; draw(); return; }
      for (let i = 0; i < 3; i++) step();
      draw();
      requestAnimationFrame(loop);
    };
    requestAnimationFrame(loop);
  }

  return { reset, sync };
})();

// 統計・Worker 表示の再描画は rAF でまとめる(イベントは 1 秒に数百件来うる)
function frame() {
  const running = uiStatus === STATUS.RUNNING || uiStatus === STATUS.STOPPING;
  if (dirty || running) render();
  raf = requestAnimationFrame(frame);
}
raf = requestAnimationFrame(frame);

// 初期表示
setStatus(STATUS.IDLE);
render();
