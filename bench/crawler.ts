// bench/crawler.ts — Go 版と同じアルゴリズムの TypeScript 実装(ROADMAP G の比較対象)。
//
// **これは出荷物ではない。** 比較のためだけに書いた、bench/ の中だけで使う実装である。
// 同じ土俵に乗せるために、次を Go 版と揃えてある:
//   - 幅優先(BFS)・URLSet による重複排除・上限ページ数
//   - Worker Pool(N 本の非同期タスクが同じキューから取り出す)
//   - 同一ドメイン制限・URL 正規化(冪等・既定ポート除去・フラグメント除去)
//   - 取得前の待ち(Request Delay)
//
// 揃っていないもの(比較を読むときに効いてくる):
//   - **HTML 解析器**。Go は golang.org/x/net/html のトークナイザ、こちらは自前の走査。
//     だから「解析時間」の欄は言語の比較ではなく**別の実装の比較**である。
//     それでも測るのは、解析が全体の何割かを示すため(割合が小さければ、
//     解析器の違いは全体の比較を汚さない、と言えるから)。
//   - ランタイム。Go はネイティブ、こちらは V8。起動時間は測定に含めない(両者とも除外)。

import { performance } from 'node:perf_hooks';

export type Config = {
  startUrl: string;
  workers: number;
  maxPages: number;
  requestDelayMs: number;
  timeoutMs: number;
};

export type Page = {
  url: string;
  statusCode: number;
  durationMs: number;
  title: string;
  error: string;
  workerId: number;
};

export type Result = {
  pages: Page[];
  links: { from: string; to: string }[];
  reason: string;
  wallMs: number;
  parseMs: number; // 解析(リンク抽出)に費やした時間の総和
  fetchMs: number; // 取得(ネットワーク待ち)に費やした時間の総和
};

/** Normalize は Go 版 crawler.Normalize と同じ規則。冪等であること。 */
export function normalize(raw: string): string | null {
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return null;
  }
  const scheme = u.protocol.replace(':', '').toLowerCase();
  if (scheme !== 'http' && scheme !== 'https') return null;
  if (!u.hostname) return null;
  u.hash = '';
  u.protocol = scheme + ':';
  u.hostname = u.hostname.toLowerCase();
  if ((scheme === 'http' && u.port === '80') || (scheme === 'https' && u.port === '443')) u.port = '';
  if (u.pathname === '') u.pathname = '/';
  return u.toString();
}

/** hostKey は host を小文字化し、既定ポートを落とす。stripWww が真なら先頭の www. も落とす。 */
function hostKey(u: URL, stripWww: boolean): string {
  let host = u.hostname.toLowerCase();
  if (stripWww) host = host.replace(/^www\./, '');
  const scheme = u.protocol.replace(':', '');
  let port = u.port;
  if ((scheme === 'http' && port === '80') || (scheme === 'https' && port === '443')) port = '';
  return port ? host + ':' + port : host;
}

/** sameDomain は Go 版 crawler.SameDomain と同じ規則(完全一致か www. の有無だけの差)。 */
export function sameDomain(start: string, link: string): boolean {
  try {
    return hostKey(new URL(start), true) === hostKey(new URL(link), true);
  } catch {
    return false;
  }
}

const SKIP_EXT = new Set([
  '.png', '.jpg', '.jpeg', '.gif', '.svg', '.webp', '.ico',
  '.pdf', '.zip', '.gz', '.tgz', '.tar', '.rar', '.7z',
  '.mp4', '.mp3', '.wav', '.avi', '.mov', '.webm',
  '.css', '.js', '.mjs', '.json', '.xml', '.rss', '.atom',
  '.woff', '.woff2', '.ttf', '.otf', '.eot',
  '.exe', '.dmg', '.apk', '.doc', '.docx', '.xls', '.xlsx', '.ppt', '.pptx',
]);

/** looksNonHtml は Go 版 crawler.LooksNonHTML と同じ(パス末尾の拡張子で判定)。 */
export function looksNonHtml(raw: string): boolean {
  let p: string;
  try {
    p = new URL(raw).pathname.toLowerCase();
  } catch {
    return false;
  }
  const i = p.lastIndexOf('.');
  if (i < 0 || p.slice(i).includes('/')) return false;
  return SKIP_EXT.has(p.slice(i));
}

const A_HREF = /<a\b[^>]*?\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/gi;
const BASE_HREF = /<base\b[^>]*?\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/i;
const TITLE = /<title[^>]*>([\s\S]*?)<\/title>/i;

/**
 * extract は <a href> と <title> を取り出す。
 * **Go 版とは別の実装**(あちらは x/net/html のトークナイザ、こちらは走査)。
 * 合成サイトの HTML に対しては同じ結果を返すことを、テストで実際に確かめている。
 */
export function extract(base: string, html: string): { links: string[]; title: string } {
  let baseUrl = base;
  const b = BASE_HREF.exec(html);
  if (b) {
    const href = b[1] ?? b[2] ?? b[3] ?? '';
    try {
      baseUrl = new URL(href, base).toString();
    } catch { /* 無視して元の base を使う */ }
  }
  const seen = new Set<string>();
  const links: string[] = [];
  A_HREF.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = A_HREF.exec(html)) !== null) {
    const href = (m[1] ?? m[2] ?? m[3] ?? '').trim();
    if (href === '' || href.startsWith('#')) continue;
    let abs: string;
    try {
      abs = new URL(href, baseUrl).toString();
    } catch {
      continue;
    }
    const n = normalize(abs);
    if (n === null || seen.has(n)) continue;
    seen.add(n);
    links.push(n);
  }
  const t = TITLE.exec(html);
  const title = t ? t[1].replace(/&amp;/g, '&').split(/\s+/).filter(Boolean).join(' ') : '';
  return { links, title };
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/**
 * run はクロールを最後まで走らせる。Go 版 Run と同じ骨格:
 * manager が inflight を数え、0 になったら終わる。Worker は N 本の非同期タスクで、
 * 同じキューから取り出す(JavaScript は 1 スレッドなので、並行なのは I/O の待ちだけ)。
 */
export async function run(cfg: Config): Promise<Result> {
  const t0 = performance.now();
  const start = normalize(cfg.startUrl);
  if (start === null) throw new Error('invalid start url: ' + cfg.startUrl);

  const seen = new Set<string>([start]);
  const queue: string[] = [start];
  const pages: Page[] = [];
  const links: { from: string; to: string }[] = [];
  const edgeSeen = new Set<string>();
  let capped = false;
  let parseMs = 0;
  let fetchMs = 0;

  // Go 版は jobs channel を閉じるまで Worker が range で待つ。JavaScript に channel は無いので、
  // 「取得中の数(inflight)」と待ち合わせの約束で同じ意味にする。
  // **キューが一時的に空でも、誰かが取得中なら待つ** —— ここを間違えると取りこぼす。
  let inflight = 0;
  let wake: (() => void) | null = null;
  const notify = () => { if (wake) { wake(); wake = null; } };
  const waitForWork = () => new Promise<void>((resolve) => {
    const prev = wake;
    wake = () => { prev?.(); resolve(); };
  });

  const worker = async (id: number): Promise<void> => {
    for (;;) {
      let url = queue.shift();
      while (url === undefined) {
        if (inflight === 0) return; // 誰も取得中でなくキューも空 = 全体が尽きた
        await waitForWork();
        url = queue.shift();
      }
      inflight++;
      try {
        await crawlOne(id, url);
      } finally {
        inflight--;
        notify();
      }
    }
  };

  const crawlOne = async (id: number, url: string): Promise<void> => {
    if (cfg.requestDelayMs > 0) await sleep(cfg.requestDelayMs);

    const started = performance.now();
    const page: Page = { url, statusCode: 0, durationMs: 0, title: '', error: '', workerId: id };
    let html = '';
    try {
      const res = await fetch(url, {
        headers: { 'User-Agent': 'GoParallelWebCrawler-bench-ts/0.1', Accept: 'text/html' },
        signal: AbortSignal.timeout(cfg.timeoutMs),
        redirect: 'follow',
      });
      page.statusCode = res.status;
      const ct = res.headers.get('content-type') ?? '';
      if (res.status < 200 || res.status > 299) {
        await res.arrayBuffer();
        page.error = 'HTTP ' + res.status;
      } else if (!/^(text\/html|application\/xhtml\+xml)/i.test(ct.trim()) && ct.trim() !== '') {
        await res.arrayBuffer();
        page.error = 'Non HTML';
      } else {
        html = await res.text();
      }
    } catch (e) {
      page.error = (e as Error).name === 'TimeoutError' ? 'Timeout' : 'Connection Error';
    }
    fetchMs += performance.now() - started;

    let extracted: string[] = [];
    if (html !== '') {
      const p0 = performance.now();
      const out = extract(url, html);
      parseMs += performance.now() - p0;
      extracted = out.links;
      page.title = out.title;
    }
    page.durationMs = Math.round(performance.now() - started);
    pages.push(page);

    for (const l of extracted) {
      if (!sameDomain(start, l) || looksNonHtml(l)) continue;
      // 区切りは連結で書く。テンプレートリテラルで「閉じ波括弧・空白・ドル」と並べると、
      // この環境では空白が NUL に化けた(2026-09-07、字種検査が捕まえた)。
      // 生の制御バイトはソースを開いても目に見えず、構文エラーにもならない
      const key = url + '\n' + l;
      if (edgeSeen.has(key)) continue;
      edgeSeen.add(key);
      links.push({ from: url, to: l });
      if (seen.has(l)) continue;
      if (seen.size >= cfg.maxPages) {
        capped = true;
        continue;
      }
      seen.add(l);
      queue.push(l);
      notify(); // 待っている Worker を起こす
    }
  };

  // Worker Pool: N 本を同時に走らせ、全員がキューを空にして戻るまで待つ。
  // Go 版と違い channel は無い(JavaScript には無い)ので、共有配列と await で表す。
  await Promise.all(Array.from({ length: cfg.workers }, (_, i) => worker(i + 1)));

  return {
    pages,
    links,
    reason: capped ? 'max_pages' : 'exhausted',
    wallMs: Math.round(performance.now() - t0),
    parseMs: Math.round(parseMs),
    fetchMs: Math.round(fetchMs),
  };
}
