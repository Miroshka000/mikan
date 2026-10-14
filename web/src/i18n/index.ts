// Keys live in code, texts in ru.json / en.json. Both files have the same shape (the
// type check in admin.ts fails the build when en.json misses a key); plural forms are
// written inside the string, ICU-style: "{n, plural, one {# день} few {# дня} other {# дней}}".
//
// Only the dictionary of the language in use is loaded, before the first render
// (initI18n), so t() stays synchronous at every call site. Each entry names what it
// loads: the admin panel the whole file (admin.ts), the subscription page the few
// sections it reads (sub.ts).
import { useSyncExternalStore } from "react";
import type ru from "./ru.json";

type Locale = "ru" | "en";
export const LOCALES: { id: Locale; label: string }[] = [
  { id: "ru", label: "Русский" },
  { id: "en", label: "English" },
];

export type Dict = typeof ru;
type Leaves<T> = { [K in keyof T & string]: T[K] extends string ? K : `${K}.${Leaves<T[K]>}` }[keyof T & string];
export type Key = Leaves<Dict>;
type Params = Record<string, string | number>;
/** How an entry fetches a language's dictionary; the module's default export is the dictionary. */
export type Loaders<D extends Partial<Dict> = Dict> = Record<Locale, () => Promise<{ default: D }>>;

const STORAGE = "mikan.lang";
let locale: Locale = detect();
let loaders: Loaders<Partial<Dict>> | undefined;
const loaded: Partial<Record<Locale, unknown>> = {};
const listeners = new Set<() => void>();

/** The visitor's own choice, then the panel's default language (the server puts it in
 * the page, see server/spa.go), then the browser's. */
function detect(): Locale {
  try {
    const saved = localStorage.getItem(STORAGE);
    if (saved === "ru" || saved === "en") return saved;
  } catch {
    // storage may be blocked; fall back to the panel's language
  }
  const panel = document.querySelector<HTMLMetaElement>('meta[name="mikan-lang"]')?.content;
  if (panel === "ru" || panel === "en") return panel;
  return navigator.languages.some((l) => l.toLowerCase().startsWith("ru")) ? "ru" : "en";
}

async function ensure(l: Locale): Promise<void> {
  if (loaded[l] || !loaders) return;
  loaded[l] = (await loaders[l]()).default;
}

/** Loads the current language's dictionary; await it before rendering anything. */
export async function initI18n(l: Loaders<Partial<Dict>>): Promise<void> {
  loaders = l;
  await ensure(locale);
  document.documentElement.lang = locale;
}

export function getLocale(): Locale {
  return locale;
}

/** Switches the language once its dictionary is here (the first time it is fetched). */
export async function setLocale(l: Locale): Promise<void> {
  if (l === locale) return;
  await ensure(l);
  locale = l;
  try {
    localStorage.setItem(STORAGE, l);
  } catch {
    // not persisted; the choice still applies to this tab
  }
  document.documentElement.lang = l;
  listeners.forEach((f) => f());
}

// At module level so React keeps one subscription per component: a function made inside the
// hook is new on every render, and the store would be left and joined again each time.
function subscribe(f: () => void): () => void {
  listeners.add(f);
  return () => listeners.delete(f);
}

/** Re-renders on a language switch: a component that reads texts follows the language itself. */
export function useLocale(): Locale {
  return useSyncExternalStore(subscribe, getLocale);
}

export function t(key: Key, params?: Params): string {
  const s = lookup(loaded[locale], key);
  if (s === undefined) {
    if (import.meta.env.DEV) console.warn(`i18n: no "${key}" in the ${locale} dictionary of this page`);
    return key;
  }
  return params ? format(s, params) : s;
}

/** For keys built at runtime (API error codes, preset ids): undefined when missing. */
export function tMaybe(key: string, params?: Params): string | undefined {
  const s = lookup(loaded[locale], key);
  return s === undefined ? undefined : params ? format(s, params) : s;
}

function lookup(d: unknown, key: string): string | undefined {
  let cur: unknown = d;
  for (const part of key.split(".")) {
    if (typeof cur !== "object" || cur === null) return undefined;
    cur = (cur as Record<string, unknown>)[part];
  }
  return typeof cur === "string" ? cur : undefined;
}

const pluralRules: Partial<Record<Locale, Intl.PluralRules>> = {};
const numberFormats: Partial<Record<Locale, Intl.NumberFormat>> = {};

function format(s: string, p: Params): string {
  return s.replace(/\{(\w+)(?:,\s*plural,\s*((?:[^{}]|\{[^{}]*\})*))?\}/g, (_, name: string, forms?: string) => {
    const v = p[name];
    if (forms === undefined) return v === undefined ? "" : String(v);
    const n = Number(v);
    const options = new Map<string, string>();
    for (const m of forms.matchAll(/(=?\w+)\s*\{([^{}]*)\}/g)) options.set(m[1]!, m[2]!);
    const rules = (pluralRules[locale] ??= new Intl.PluralRules(locale));
    const body = options.get(`=${n}`) ?? options.get(rules.select(n)) ?? options.get("other") ?? "";
    const nf = (numberFormats[locale] ??= new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }));
    return body.replace(/#/g, nf.format(n));
  });
}
