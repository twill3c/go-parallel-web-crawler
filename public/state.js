// state.js — SSE パーサと画面状態の reducer(純関数・DOM を触らない)。
// app.js(ブラウザ)と tests/ui/reducer.test.mjs(Node)の両方から同じコードを使う(SPEC F-36 / G-09)。

/**
 * createSSEParser は text/event-stream をイベントに分ける。
 * fetch の ReadableStream は任意の位置で切れて届くので、行の途中・ブロックの途中を跨いで状態を持つ。
 * 一括で届いても 1 文字ずつ届いても同じイベント列になる(G-09)。
 * @param {(event: object) => void} onEvent
 */
export function createSSEParser(onEvent) {
  let buffer = '';
  let name = '';
  let data = [];

  function dispatch() {
    if (data.length === 0) {
      name = '';
      return;
    }
    const text = data.join('\n');
    name = '';
    data = [];
    let obj;
    try {
      obj = JSON.parse(text);
    } catch {
      return; // 壊れた data は捨てる。後続は生きる
    }
    if (obj && typeof obj === 'object') onEvent(obj);
  }

  function handleLine(line) {
    if (line === '') {
      dispatch();
      return;
    }
    if (line.startsWith(':')) return; // コメント
    const i = line.indexOf(':');
    const field = i < 0 ? line : line.slice(0, i);
    let value = i < 0 ? '' : line.slice(i + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') name = value;
    else if (field === 'data') data.push(value);
  }

  return {
    push(chunk) {
      buffer += chunk;
      let nl;
      while ((nl = buffer.indexOf('\n')) >= 0) {
        let line = buffer.slice(0, nl);
        if (line.endsWith('\r')) line = line.slice(0, -1);
        buffer = buffer.slice(nl + 1);
        handleLine(line);
      }
    },
    end() {
      if (buffer.length > 0) {
        handleLine(buffer.replace(/\r$/, ''));
        buffer = '';
      }
      dispatch();
    },
  };
}

/** Worker の状態(SPEC F-13 / 原本 §11): waiting → crawling → (error →) waiting → completed */
export const WORKER = { WAITING: 'waiting', CRAWLING: 'crawling', ERROR: 'error', COMPLETED: 'completed' };

/** 全体の状態(原本 §11) */
export const STATUS = { IDLE: 'idle', RUNNING: 'running', STOPPING: 'stopping', COMPLETED: 'completed', ERROR: 'error' };

export function initialState() {
  return {
    status: STATUS.IDLE,
    crawlId: '',
    startUrl: '',
    config: { workers: 0, maxPages: 0, requestDelayMs: 0 },
    t: 0, // 最後に受けたイベントの t(ms)
    workers: {}, // id → { id, status, url, pages, lastError }
    pages: [], // model.Page の列(到着順)
    links: [], // { from, to } 一意
    nodes: new Map(), // url → { url, fetched, statusCode, error, order, parent }
    discovered: 0, // URLSet に入った URL の数(開始 URL を含む)。nodes はこれより多くなりうる(上限で切られた先)
    stats: null, // crawl_completed の statistics
    reason: '',
    error: '',
  };
}

/**
 * reduce は 1 イベントを状態に畳み込む。state を破壊的に更新して返す(描画側は差分を見ない)。
 * イベントの形は SPEC §5。
 */
export function reduce(state, e) {
  if (typeof e.t === 'number' && e.t > state.t) state.t = e.t;
  switch (e.type) {
    case 'crawl_started': {
      state.status = STATUS.RUNNING;
      state.crawlId = e.crawlId || '';
      state.startUrl = e.url || '';
      state.config = { workers: e.workers || 0, maxPages: e.maxPages || 0, requestDelayMs: e.requestDelayMs || 0 };
      state.workers = {};
      for (let id = 1; id <= state.config.workers; id++) {
        state.workers[id] = { id, status: WORKER.WAITING, url: '', pages: 0, lastError: '' };
      }
      state.pages = [];
      state.links = [];
      state.nodes = new Map();
      state.discovered = 0;
      state.stats = null;
      state.reason = '';
      state.error = '';
      if (state.startUrl) {
        addNode(state, state.startUrl, '').queued = true;
        state.discovered = 1; // 開始 URL は最初から URLSet に入っている
      }
      break;
    }
    case 'worker_started': {
      const w = worker(state, e.workerId);
      w.status = WORKER.CRAWLING;
      w.url = e.url || '';
      break;
    }
    case 'page_completed': {
      const w = worker(state, e.workerId);
      const page = {
        url: e.url || '',
        statusCode: e.statusCode || 0,
        durationMs: e.durationMs || 0,
        title: e.title || '',
        error: e.error || '',
        workerId: e.workerId || 0,
      };
      state.pages.push(page);
      w.pages += 1;
      w.status = page.error ? WORKER.ERROR : WORKER.WAITING;
      w.lastError = page.error;
      w.url = page.error ? page.url : '';
      const n = addNode(state, page.url, '');
      n.fetched = true;
      n.statusCode = page.statusCode;
      n.error = page.error;
      break;
    }
    case 'link_found': {
      if (!e.from || !e.to) break;
      state.links.push({ from: e.from, to: e.to, queued: !!e.queued });
      const n = addNode(state, e.to, e.from);
      if (e.queued && !n.queued) {
        n.queued = true;
        state.discovered += 1; // URLSet に入った(= jobs channel に送られた)URL の数
      }
      break;
    }
    case 'worker_done': {
      const w = worker(state, e.workerId);
      w.status = WORKER.COMPLETED;
      w.url = '';
      break;
    }
    case 'crawl_completed': {
      state.status = STATUS.COMPLETED;
      state.reason = e.reason || '';
      state.stats = e.statistics || null;
      break;
    }
    default:
      break;
  }
  return state;
}

function worker(state, id) {
  if (!state.workers[id]) {
    state.workers[id] = { id, status: WORKER.WAITING, url: '', pages: 0, lastError: '' };
  }
  return state.workers[id];
}

function addNode(state, url, parent) {
  let n = state.nodes.get(url);
  if (!n) {
    // queued: URLSet に入った(取得予定 or 取得済み)。false のままなら「発見したが上限で取らない」URL
    n = { url, fetched: false, queued: false, statusCode: 0, error: '', order: state.nodes.size, parent };
    state.nodes.set(url, n);
  }
  return n;
}

/**
 * liveStats は進行中の暫定統計(サーバの statistics が来る前の表示用)。
 * 完了後はサーバの値を使う(G-08 はサーバ側と Go テストで照合済み)。
 */
export function liveStats(state) {
  if (state.stats) return state.stats;
  const pages = state.pages;
  const total = pages.length;
  const success = pages.filter((p) => !p.error).length;
  const durationMs = state.t;
  const durs = pages.map((p) => p.durationMs).sort((a, b) => a - b);
  const avgMs = total ? Math.floor(durs.reduce((a, b) => a + b, 0) / total) : 0;
  const p95Ms = total ? durs[Math.ceil(0.95 * total) - 1] : 0;
  const requestsPerSec = durationMs > 0 ? Math.round((total / (durationMs / 1000)) * 10) / 10 : 0;
  return { total, success, errors: total - success, durationMs, requestsPerSec, avgMs, p95Ms };
}

/** channel の中身: キュー待ち(送られたが取り出されていない)/ 取得中 / 完了 */
export function channelCounts(state) {
  const crawling = Object.values(state.workers).filter((w) => w.status === WORKER.CRAWLING).length;
  const done = state.pages.length;
  const queued = Math.max(0, state.discovered - done - crawling);
  return { queued, crawling, done, discovered: state.discovered };
}
