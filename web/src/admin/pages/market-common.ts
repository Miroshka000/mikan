import { Bot, LayoutPanelTop, Puzzle, type LucideIcon } from "lucide-react";
import { api, unwrap, type Schemas } from "../../api/client";
import { t, type Key } from "../../i18n";
import { localized } from "../../lib/localized";
import type { State } from "./addon-row";

// The protocol 2 addons' data and how an addon is named: what the addons page, its dialogs
// and the addon pages share.

export type Market = Schemas["MarketView"];
export type MarketEntry = Schemas["MarketEntry"];
export type MarketInstalled = Schemas["MarketInstalled"];
export type MarketOrigin = Schemas["MarketOrigin"];
export type Kind = MarketInstalled["kind"];
type Payments = Schemas["AddonsView"];
type Telegram = Schemas["TelegramView"];

/** What an addon's id may be, as the server has it. */
export const ADDON_ID_RE = /^[a-z0-9][a-z0-9-]{0,31}$/;

/** The host takes requests: the panel supports addons and the host is new enough. */
export function marketReady(m: Market | undefined): boolean {
  return !!m && m.supported && m.host_ready;
}

/** Every addon the server has a request for: its actions wait until the server is done. */
export function queuedIds(m: Market | undefined, p: Payments | undefined): Set<string> {
  return new Set(serverQueue(m, p).map((r) => r.id));
}

/** The server's one queue of requests, oldest first: both lists carry it. */
export function serverQueue(m: Market | undefined, p: Payments | undefined) {
  return m?.queue ?? p?.queue ?? [];
}

/** Install (or update) an addon of the catalog with the permissions its entry asks for. */
export function installMarket(e: MarketEntry) {
  return unwrap(api.POST("/api/v1/marketplace/{id}/install", { params: { path: { id: e.id } }, body: { permissions: e.permissions } }));
}

/** Install (or update) a payment method from the catalog. */
export function installPayment(id: string) {
  return unwrap(api.POST("/api/v1/addons/{id}/install", { params: { path: { id } } }));
}

/** How the panel's bot is doing; the words are the caller's. */
export type BotPhase = "off" | "ok" | "reconnecting" | "starting" | "down";

/** The bot of a set-up panel by its connection: on or off, running, reconnecting or down. */
export function botStatus(tg: Telegram, words: Record<BotPhase, Key>): State {
  const at = (tone: State["tone"], phase: BotPhase): State => ({ tone, text: t(words[phase]) });
  if (!tg.enabled) return at("off", "off");
  if (tg.running && !tg.error) return at("ok", "ok");
  if (tg.running) return at("warn", "reconnecting");
  if (!tg.error) return at("off", "starting");
  return at("bad", "down");
}

/**
 * The entry that offers an installed addon's updates: the signed catalog's for an official
 * one, its own source's for one from a source. None for an addon of the admin's own (an image
 * from before sources, a process of theirs): only they know its next build.
 */
export function entryOf(id: string, d: Market | undefined): MarketEntry | undefined {
  const a = d?.installed.find((x) => x.id === id);
  if (a && a.source !== "official" && !a.origin) return undefined;
  return d?.catalog.find((e) => e.id === id && (e.origin?.id ?? 0) === (a?.origin?.id ?? 0));
}

/** The badge of an unofficial addon: whose it is, short enough for a phone. */
export function unofficialLabel(o: MarketOrigin): string {
  const title = o.title.length > 24 ? `${o.title.slice(0, 23)}…` : o.title;
  return t("market.unofficial", { title });
}

/** The addon's name in the catalog; an addon of the admin's own is known by its id. */
export function marketName(id: string, d: Market | undefined): string {
  return localized(entryOf(id, d)?.name, id);
}

export const KIND_ICONS: Record<Kind, LucideIcon> = { bot: Bot, ui: LayoutPanelTop, generic: Puzzle };

/** Every permission the panel knows, in the order the admin reads them, with its words. */
export const PERMISSIONS: { id: string; label: Key; text: Key }[] = [
  { id: "bot", label: "market.perm.bot", text: "market.perm.botText" },
  { id: "users:read", label: "market.perm.usersRead", text: "market.perm.usersReadText" },
  { id: "users:write", label: "market.perm.usersWrite", text: "market.perm.usersWriteText" },
  { id: "users:all", label: "market.perm.usersAll", text: "market.perm.usersAllText" },
  { id: "users:grant", label: "market.perm.usersGrant", text: "market.perm.usersGrantText" },
  { id: "billing:invoice", label: "market.perm.invoice", text: "market.perm.invoiceText" },
  { id: "events", label: "market.perm.events", text: "market.perm.eventsText" },
  { id: "kv", label: "market.perm.kv", text: "market.perm.kvText" },
  { id: "ui", label: "market.perm.ui", text: "market.perm.uiText" },
];

/** In the order of PERMISSIONS; one the panel does not know stays last, by its name. */
export function sortPermissions(perms: string[]): string[] {
  const at = (p: string) => {
    const i = PERMISSIONS.findIndex((x) => x.id === p);
    return i < 0 ? PERMISSIONS.length : i;
  };
  return [...perms].sort((a, b) => at(a) - at(b) || a.localeCompare(b));
}

/**
 * Talking to the clients for the panel, giving them days (any of them, for users:grant) or
 * reading all of them (users:all): the addon is as good as an admin.
 */
export const DANGEROUS_PERMISSIONS = ["bot", "users:write", "users:all", "users:grant"];

export function actsAsAdmin(perms: string[]): boolean {
  return perms.some((p) => DANGEROUS_PERMISSIONS.includes(p));
}

/** Its pages in the admin panel: granted and running. */
export function hasPages(a: MarketInstalled): boolean {
  return a.status === "running" && a.granted.includes("ui");
}

/** Where the addon's own pages open (the route of the addon pages, a plain path here). */
export function pagesPath(id: string): string {
  return `/addons/app/${encodeURIComponent(id)}`;
}
