import { getLocale, t } from "../i18n";

export const GiB = 1024 ** 3;

// Formatters follow the UI language; the apps remount on a switch, so caching per call
// site is not needed, only per locale.
const cache = new Map<string, Intl.NumberFormat | Intl.DateTimeFormat>();
function nf(max: number): Intl.NumberFormat {
  const key = `n${getLocale()}${max}`;
  let f = cache.get(key) as Intl.NumberFormat | undefined;
  if (!f) cache.set(key, (f = new Intl.NumberFormat(getLocale(), { maximumFractionDigits: max })));
  return f;
}
function df(opts: Intl.DateTimeFormatOptions, id: string): Intl.DateTimeFormat {
  const key = `d${getLocale()}${id}`;
  let f = cache.get(key) as Intl.DateTimeFormat | undefined;
  if (!f) cache.set(key, (f = new Intl.DateTimeFormat(getLocale(), opts)));
  return f;
}

function scaled(v: number, units: string[], step: number, first: string): string {
  let x = v;
  let i = -1;
  // A value that rounds up to the step ("1,024 KB") is said in the next unit.
  while (Math.round(x) >= step && i < units.length - 1) {
    x /= step;
    i++;
  }
  if (i < 0) return `${nf(0).format(x)} ${first}`;
  return `${x >= 100 ? nf(0).format(x) : nf(1).format(x)} ${units[i]}`;
}

export function bytes(n: number): string {
  return scaled(n, [t("units.kb"), t("units.mb"), t("units.gb"), t("units.tb"), t("units.pb")], 1024, t("units.b"));
}

export function bits(bps: number): string {
  return scaled(bps, [t("units.kbps"), t("units.mbps"), t("units.gbps")], 1000, t("units.bps"));
}

/** A period of days in words, as in "once a …": "сутки", "неделю", "месяц", "N дней". The
 * Mini App's texts: its bundle has them, the admin's too. */
export function period(days: number): string {
  if (days === 1) return t("sub.periodDay");
  if (days === 7) return t("sub.periodWeek");
  if (days === 30) return t("sub.periodMonth");
  return t("sub.periodDays", { n: days });
}

/** The same as a label of its own, a chip: "Сутки", "Неделю". */
export function periodLabel(days: number): string {
  const p = period(days);
  return p.charAt(0).toLocaleUpperCase(getLocale()) + p.slice(1);
}

export function num(n: number): string {
  return nf(0).format(n);
}

export const days = (n: number) => t("time.days", { n });

export const months = (n: number) => t("time.months", { n });

/** A tariff's term in months when it runs to a billing day; the server counts 30 days as one. */
export const termMonths = (durationDays: number) => Math.max(1, Math.floor((durationDays + 15) / 30));

export function dateShort(iso: string): string {
  return df({ day: "numeric", month: "short" }, "short").format(new Date(iso));
}

export function dateLong(iso: string): string {
  return df({ day: "numeric", month: "long", year: "numeric" }, "long").format(new Date(iso));
}

/** A moment as "21 октября, 14:05": a certificate's term, when an order may go again. */
export function dateTime(iso: string): string {
  return df({ day: "numeric", month: "long", hour: "2-digit", minute: "2-digit", hour12: false }, "datetime").format(new Date(iso));
}

const pad2 = (n: number) => String(n).padStart(2, "0");

/** The local calendar date of iso as an <input type="date"> value. */
export function inputDate(iso: string): string {
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

/** An <input type="date"> value at the local time of day of clock (now without one). */
export function fromInputDate(date: string, clock?: string | null): string {
  const [y, m, d] = date.split("-").map(Number);
  const c = clock ? new Date(clock) : new Date();
  const dt = new Date(y!, m! - 1, d!, c.getHours(), c.getMinutes(), c.getSeconds());
  if (dt.getUTCFullYear() >= 10000) {
    return "9999-12-31T23:59:59.000Z";
  }
  return dt.toISOString();
}

export function time(iso: string): string {
  return df({ hour: "2-digit", minute: "2-digit", hour12: false }, "time").format(new Date(iso));
}

/** A moment's local calendar day as a whole number, so days between are counted by dates: a clock change makes a day 23 or 25 hours. */
const calendarDay = (ms: number) => {
  const d = new Date(ms);
  return Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()) / 86_400_000;
};

/**
 * Days from today to the day of iso by the local calendar: 0 is today, 1 tomorrow, negative for
 * earlier days. Whether the moment itself is over is another matter (earlier today is 0 as well).
 */
export function daysUntil(iso: string, now = Date.now()): number {
  return calendarDay(new Date(iso).getTime()) - calendarDay(now);
}

/**
 * The days a term that waits for its first connection will have: its end moves on with the
 * clock every ten minutes, so the days are rounded from the time left. Counted by the calendar
 * they would drop a day for ten minutes past midnight, when the last move was before it.
 */
export function waitingDays(iso: string, now = Date.now()): number {
  return Math.max(0, Math.round((new Date(iso).getTime() - now) / 86_400_000));
}

export function expiryText(iso: string | null | undefined, now = Date.now()): { text: string; tone: "" | "warn" | "bad" } {
  if (!iso) return { text: t("time.forever"), tone: "" };
  const d = daysUntil(iso, now);
  // Over is decided by the moment, not by the day count: a few hours ago is day 0, like a few hours ahead.
  if (new Date(iso).getTime() < now) return { text: d < 0 ? t("time.expiredAgo", { n: -d }) : t("time.expiredToday"), tone: "bad" };
  if (d === 0) return { text: t("time.today"), tone: "warn" };
  if (d === 1) return { text: t("time.tomorrow"), tone: "warn" };
  return { text: days(d), tone: d <= 7 ? "warn" : "" };
}

export function ago(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return t("time.justNow");
  const m = Math.round(s / 60);
  if (m < 60) return t("time.minAgo", { n: m });
  const h = Math.round(m / 60);
  if (h < 24) return t("time.hAgo", { n: h });
  return t("time.daysAgo", { n: Math.round(h / 24) });
}

/** What is left until iso, in the words uptime uses ("1 ч 12 мин"); under a minute is said so. */
export function timeLeft(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.floor((new Date(iso).getTime() - now) / 1000));
  if (s < 60) return t("time.lessMinute");
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return t("time.uptimeDays", { d, h });
  if (h > 0) return t("time.uptimeHours", { h, m });
  return t("time.uptimeMinutes", { m });
}

export function uptime(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return t("time.uptimeDays", { d, h });
  if (h > 0) return t("time.uptimeHours", { h, m });
  return t("time.uptimeMinutes", { m });
}

/** The app a User-Agent names: its first product token that is not the core itself ("mihomo/1.19.32 ClashFest/1.2.0" is ClashFest). */
export function appName(ua: string): string {
  const tokens = ua.trim().split(/\s+/).filter(Boolean);
  const app = tokens.find((t) => !/^(mihomo|clash[.-]?meta)(\/|;|$)/i.test(t)) ?? tokens[0] ?? "";
  return app.replace("/", " ");
}

/** The host of a REALITY dest ("host:port") is an IP: clients then need a site name (SNI) of their own. */
export function destIsIP(dest: string): boolean {
  const host = dest
    .trim()
    .replace(/:\d*$/, "")
    .replace(/^\[(.*)\]$/, "$1");
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(":");
}

/** The site an inbound looks like from outside: the name clients send, not the IP the node relays probes to. */
export function maskedAs(i: { dest?: string; server_names?: string[] }): string {
  return i.server_names?.[0] || (i.dest ?? "").replace(/:443$/, "");
}

/** Keeps the network part of an IP readable and hides the rest in lists. */
export function maskIP(ip: string): string {
  if (ip.includes(":")) return ip.split(":").slice(0, 3).join(":") + ":…";
  const p = ip.split(".");
  return p.length === 4 ? `${p[0]}.${p[1]}.•••.•••` : ip;
}

/** Kopecks as rubles: 19900 → "199 ₽", 19950 → "199,50 ₽". */
export function rubles(kopecks: number): string {
  const min = kopecks % 100 ? 2 : 0;
  const key = `r${getLocale()}${min}`;
  let f = cache.get(key) as Intl.NumberFormat | undefined;
  if (!f) cache.set(key, (f = new Intl.NumberFormat(getLocale(), { minimumFractionDigits: min, maximumFractionDigits: 2 })));
  return `${f.format(kopecks / 100)} ₽`;
}

/** How a balance's currency shows; another shows by its code. */
const CURRENCY_SIGNS: Record<string, string> = { USD: "$", EUR: "€", UAH: "₴", BYN: "Br", KZT: "₸", UZS: "сўм", USDT: "USDT" };

/** A payment's sum: Stars, kopecks, or hundredths of a balance's currency. */
export function money(amount: number, currency: string): string {
  if (currency === "XTR") return `⭐ ${num(amount)}`;
  if (currency === "RUB" || !currency) return rubles(amount);
  const min = amount % 100 ? 2 : 0;
  const key = `c${getLocale()}${min}`;
  let f = cache.get(key) as Intl.NumberFormat | undefined;
  if (!f) cache.set(key, (f = new Intl.NumberFormat(getLocale(), { minimumFractionDigits: min, maximumFractionDigits: 2 })));
  return `${f.format(amount / 100)} ${CURRENCY_SIGNS[currency] ?? currency}`;
}

/**
 * An hour the server keeps in UTC, as this browser's local time; the UTC hour follows in
 * brackets when the two differ, so nobody has to count the time zone.
 */
export function utcHourLabel(h: number): string {
  const d = new Date(Date.UTC(2000, 0, 1, h));
  const local = d.getHours();
  const pad = (x: number) => String(x).padStart(2, "0");
  return local === h ? `${pad(h)}:00` : `${pad(local)}:00 (UTC ${pad(h)}:00)`;
}
