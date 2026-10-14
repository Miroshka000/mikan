import { safeHref } from "../lib/url";

// The same subscription URL serves the config to apps; the page only links to it. In
// Telegram the page runs as the bot's Mini App at /<sub path>/tg: the subscription then
// comes from Telegram's sign-in instead of the address.
export const pageURL = location.origin + location.pathname.replace(/\/$/, "");
export const tgMode = /\/tg$/.test(pageURL);
export const subRoot = pageURL.replace(/\/tg$/, "");
// What follows the #. Telegram puts its launch data there; an address with its own anchor
// (the bot's "Promo codes" opens #promocodes) gets the data after a "?" instead, the way
// telegram-web-app.js reads it: #promocodes?tgWebAppData=…
const hash = location.hash.slice(1);
const query = hash.indexOf("?");
const head = query >= 0 ? hash.slice(0, query) : (hash.split("&")[0] ?? "");
/** The anchor the page was opened at, without Telegram's data: "promocodes". */
export const hashAnchor = head.includes("=") ? "" : decodeAnchor(head);
/** The key=value pairs after the #. */
export const hashParams = new URLSearchParams(query >= 0 ? hash.slice(query + 1) : hash);
// Telegram's launch data: the Mini App's sign-in.
export const initData = hashParams.get("tgWebAppData") ?? "";

function decodeAnchor(s: string) {
  try {
    return decodeURIComponent(s);
  } catch {
    return "";
  }
}

/** The token a subscription URL ends with. */
export const tokenOf = (url: string) => url.slice(url.lastIndexOf("/") + 1);

const TIMEOUT_MS = 15_000;

/**
 * fetch that never hangs: a request that does not answer in 15 s fails like any other
 * (the page then shows its error instead of skeletons forever), and `signal` cancels it.
 */
export function request(url: string, init: RequestInit & { signal?: AbortSignal } = {}): Promise<Response> {
  const ctl = new AbortController();
  const timer = window.setTimeout(() => ctl.abort(), TIMEOUT_MS);
  init.signal?.addEventListener("abort", () => ctl.abort(), { once: true });
  return fetch(url, { cache: "no-store", ...init, signal: ctl.signal }).finally(() => window.clearTimeout(timer));
}

export function json<T>(r: Response): Promise<T> {
  return r.ok ? (r.json() as Promise<T>) : Promise.reject(new Error(String(r.status)));
}

/** What a JSON POST came back with: the status and the body ({} when it is not JSON). */
type Posted<T> = { ok: boolean; status: number; data: Partial<T> };

/**
 * A POST with a JSON body (none when `body` is undefined) over request(), so it has the same
 * 15 s limit. A refusal does not throw: the server's reason sits in `data` ({ code } for the
 * page to name), only a network failure or a timeout rejects.
 */
export async function postJSON<T>(url: string, body?: unknown, signal?: AbortSignal): Promise<Posted<T>> {
  const r = await request(url, { method: "POST", signal, ...(body === undefined ? {} : { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }) });
  const data = (await r.json().catch(() => ({}))) as Partial<T>;
  return { ok: r.ok, status: r.status, data };
}

/** The body of an answer that has to be a success: any other status rejects, as json() does. */
export function okData<T>(p: Posted<T>): T {
  if (!p.ok) throw new Error(String(p.status));
  return p.data as T;
}

type TelegramProxy = { postEvent?: (type: string, data: string) => void };

/** Telegram's Mini App bridge: to the app's web view, or to Telegram Web around the frame. */
export function tgEvent(type: string, data: Record<string, unknown> | "" = "") {
  const w = window as Window & { TelegramWebviewProxy?: TelegramProxy };
  if (w.TelegramWebviewProxy?.postEvent) w.TelegramWebviewProxy.postEvent(type, JSON.stringify(data));
  else if (window.parent !== window) window.parent.postMessage(JSON.stringify({ eventType: type, eventData: data }), "https://web.telegram.org");
}

/** Opens a link outside the Mini App: Telegram links in Telegram, the rest in the browser. */
export function openOutside(url: string) {
  // t.me/<name>?start=… with any path or query, over http too: Telegram opens it itself.
  const tme = /^https?:\/\/(?:www\.)?(?:t|telegram)\.me(\/.*)$/i.exec(url);
  if (!tgMode) {
    // The same page in a browser has no bridge: a plain link in a new tab.
    const href = safeHref(url);
    if (href) window.open(href, "_blank", "noopener,noreferrer");
  } else if (tme) tgEvent("web_app_open_tg_link", { path_full: tme[1] });
  else if (safeHref(url, { tg: true })) tgEvent("web_app_open_link", { url });
}

/** In Telegram a link leaves the Mini App through the bridge; elsewhere it is a plain link. */
export function outside(url: string) {
  return tgMode
    ? {
        onClick: (e: React.MouseEvent) => {
          e.preventDefault();
          openOutside(url);
        },
      }
    : {};
}
