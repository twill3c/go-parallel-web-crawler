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
    robots: '', // obeyed / absent / unreachable / ignored(SPEC §5)
    crawlDelayMs: 0, // robots.txt の Crawl-delay
    robotsBlocked: 0, // robots.txt が拒否して辿らなかった URL の数
    sitemap: '', // used / absent / declared_missing / skipped(原本 §34 B)
    sitemapUrls: 0, // サイトマップが挙げていた URL の数
    external: new Map(), // host → { host, links:Set, fromPages:Set }(原本 §34 C・辿らない)
  };
}

/** externalDomains は外部ドメインを件数の多い順(同数ならホスト名順)に返す。 */
export function externalDomains(state) {
  return [...state.external.values()]
    .map((d) => ({ host: d.host, links: d.links.size, fromPages: d.fromPages.size }))
    .sort((a, b) => (b.links - a.links) || a.host.localeCompare(b.host));
}

/** externalTotal は外部への一意な URL の総数。 */
export function externalTotal(state) {
  let n = 0;
  for (const d of state.external.values()) n += d.links.size;
  return n;
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
      state.robots = e.robots || '';
      state.crawlDelayMs = e.crawlDelayMs || 0;
      state.robotsBlocked = 0;
      state.sitemap = e.sitemap || '';
      state.sitemapUrls = e.sitemapUrls || 0;
      state.external = new Map();
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
        description: e.description || '',
        h1: e.h1 || '',
        canonical: e.canonical || '',
        finalUrl: e.finalUrl || '',
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
    case 'external_found': {
      // 別ドメインへのリンク(原本 §34 C)。**辿らない。数えるだけ。**
      if (!e.from || !e.to) break;
      let host;
      try {
        host = new URL(e.to).hostname.toLowerCase().replace(/^www\./, '');
      } catch {
        break;
      }
      if (!host) break;
      let d = state.external.get(host);
      if (!d) {
        d = { host, links: new Set(), fromPages: new Set() };
        state.external.set(host, d);
      }
      d.links.add(e.to);
      d.fromPages.add(e.from);
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
      state.robotsBlocked = e.robotsBlocked || 0;
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

// ---------- Go と TypeScript の比較(ROADMAP G)----------

/**
 * langRows は bench-results.json を画面の行に畳む。
 * **比較が成立していない行では倍率を出さない。** 出さない理由も一緒に返す(HC-079)。
 * 成立の条件は三つ:
 *   sameResult  両実装が同じページ数・同じ完了理由に達した
 *   !siteBound  合成サイトが律速でない(Workers を増やした効きが出ている)
 *   !noisy      どちらの中央値も 500ms 以上(速すぎる計測は機械のばらつきを測る)
 */
export function langRows(data) {
  if (!data || !Array.isArray(data.crawl)) return [];
  return data.crawl.map((c) => {
    const reasons = [];
    if (!c.sameResult) reasons.push('取得結果が食い違った');
    if (c.siteBound) reasons.push(`合成サイトが律速(Workers ${c.params.workers} で ${c.goScaling} 倍しか出ていない)`);
    if (c.noisy) reasons.push('計測が短すぎる(500ms 未満)');
    return {
      label: c.label,
      goMs: c.wallMs.go,
      tsMs: c.wallMs.ts,
      goSpread: c.spreadMs?.go ?? null,
      tsSpread: c.spreadMs?.ts ?? null,
      pages: c.pages.go,
      comparable: !!c.comparable,
      ratio: c.comparable ? c.ratio : null,
      whyNot: reasons.join(' / '),
    };
  });
}

/** langVerdict は比較できた行だけから一文をまとめる。無ければ null。 */
export function langVerdict(data) {
  const rows = langRows(data).filter((r) => r.comparable);
  if (rows.length === 0) return null;
  const ratios = rows.map((r) => r.ratio);
  const lo = Math.min(...ratios);
  const hi = Math.max(...ratios);
  const pct = (x) => `${Math.round(Math.abs(x - 1) * 100)}%`;
  return {
    rows: rows.length,
    lo,
    hi,
    text: hi <= 1.3
      ? `ネットワーク待ちのあるクロールでは、両者の差は ${pct(lo)}〜${pct(hi)} に収まりました。待つ時間が支配的で、言語の差はそこに埋もれます。`
      : `比較できた ${rows.length} 条件で、TypeScript 版は Go 版の ${lo}〜${hi} 倍の時間でした。`,
  };
}

// ---------- ページの状態と参照元(ROADMAP D)----------

/**
 * statusClass は HTTP 状態とエラー種別から画面の区分を返す(Go 側の StatusClass と同じ規則)。
 * 色に頼らず記号と文字でも読めるようにするため、区分名を返して見せ方は呼び手に任せる。
 */
export function statusClass(statusCode, error) {
  switch (error) {
    case 'Timeout': case 'DNS Error': case 'Connection Error':
    case 'Too Many Redirects': case 'Redirect off-domain': case 'Forbidden':
      return 'failed';
    default: break;
  }
  if (statusCode >= 200 && statusCode < 300 && !error) return 'ok';
  if (statusCode >= 300 && statusCode < 400) return 'redirect';
  if (statusCode >= 400 && statusCode < 500) return 'missing';
  if (statusCode >= 500) return 'server';
  return 'other';
}

/** 区分ごとの記号と読み。色が見えなくても区別できる(原本 §17 の流儀) */
export const STATUS_SYMBOL = {
  ok: { sym: '✓', label: 'OK' },
  redirect: { sym: '→', label: '転送' },
  missing: { sym: '✕', label: '不在' },
  server: { sym: '!', label: '障害' },
  failed: { sym: '✕', label: '到達せず' },
  other: { sym: '·', label: 'その他' },
};

/**
 * referrers は「その URL を指しているページ」を辺の集合から逆引きする(ROADMAP D)。
 * リンク切れを直すのは参照元の側なので、壊れた URL には必ずこれを添える。
 */
export function referrers(state, url) {
  const out = [];
  for (const l of state.links) {
    if (l.to === url && !out.includes(l.from)) out.push(l.from);
  }
  return out;
}

/** brokenPages は取得できなかった・見つからなかったページを、参照元つきで返す */
export function brokenPages(state) {
  return state.pages
    .filter((p) => ['missing', 'server', 'failed'].includes(statusClass(p.statusCode, p.error)))
    .map((p) => ({ ...p, class: statusClass(p.statusCode, p.error), from: referrers(state, p.url) }));
}

// ---------- ベンチマーク(F-38 / 原本 §21)----------

/** ベンチで試す Workers の列。SPEC §2.5 の上限 20 を超えない */
export const BENCH_WORKERS = [1, 2, 5, 10];

/**
 * benchRow は 1 回のクロールの statistics から表の 1 行を作る。
 * pagesPerSec はサーバの requestsPerSec をそのまま使わず、同じ式(total / (durationMs/1000))で
 * 割り直す。二つが食い違えば、どちらかが SPEC §5 の定義から外れている。
 */
export function benchRow(workers, st, reason) {
  const seconds = st.durationMs / 1000;
  const pagesPerSec = seconds > 0 ? Math.round((st.total / seconds) * 10) / 10 : 0;
  return {
    workers,
    pages: st.total,
    errors: st.errors,
    seconds,
    durationMs: st.durationMs,
    pagesPerSec,
    avgMs: st.avgMs,
    p95Ms: st.p95Ms,
    reason,
  };
}

/**
 * MIN_BENCH_MS は「比べてよい」と言うために各行が要する最低の所要時間。
 * これより速いクロールでは、差が並行度でなく往復のばらつきで決まる。
 * 実測(2026-09-07・ローカル合成サイト): 全行 20〜50 ms のとき Workers 1 が 444 pages/s、
 * 5 が 750、10 が 267 と**単調ですらない**値が出た。数を出せば読み手はそれを結果として読むので、
 * この下限を割ったら速度比も最速の印も出さない(HC-079)。
 */
export const MIN_BENCH_MS = 500;

/**
 * benchSummary は行の集合から、最速・基準(Workers 最小)・速度比・棒の割合を出す。
 *
 * 速度比を出してよいのは二つの前提が揃ったときだけ:
 *   comparable — 全行が同じページ数を取れた(揃わなければ時間を並べても比較にならない)
 *   longEnough — 最も遅い行が MIN_BENCH_MS 以上かかった(短すぎる計測は雑音)
 * どちらかが崩れていれば、画面は数を出さずに理由を書く(HC-079: 裏づけの無い数・記号を出さない)。
 */
export function benchSummary(rows) {
  if (rows.length === 0) {
    return {
      rows, fastest: null, baseline: null, maxPagesPerSec: 0,
      comparable: false, longEnough: false, trustworthy: false,
      speedup: () => 1, bar: () => 0,
    };
  }
  const maxPagesPerSec = Math.max(...rows.map((r) => r.pagesPerSec));
  const fastest = rows.reduce((a, b) => (b.pagesPerSec > a.pagesPerSec ? b : a));
  const baseline = rows.reduce((a, b) => (b.workers < a.workers ? b : a));
  const comparable = rows.length > 1 && rows.every((r) => r.pages === rows[0].pages);
  const longEnough = rows.every((r) => r.durationMs >= MIN_BENCH_MS);
  return {
    rows,
    fastest,
    baseline,
    maxPagesPerSec,
    comparable,
    longEnough,
    trustworthy: comparable && longEnough,
    speedup: (r) => (baseline.pagesPerSec > 0 ? Math.round((r.pagesPerSec / baseline.pagesPerSec) * 10) / 10 : 1),
    bar: (r) => (maxPagesPerSec > 0 ? r.pagesPerSec / maxPagesPerSec : 0),
  };
}

/** channel の中身: キュー待ち(送られたが取り出されていない)/ 取得中 / 完了 */
export function channelCounts(state) {
  const crawling = Object.values(state.workers).filter((w) => w.status === WORKER.CRAWLING).length;
  const done = state.pages.length;
  const queued = Math.max(0, state.discovered - done - crawling);
  return { queued, crawling, done, discovered: state.discovered };
}
