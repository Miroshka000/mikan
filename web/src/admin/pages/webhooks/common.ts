// What the webhooks page's parts share: the types of the API, the keys of its queries, and the
// words for what the API says in codes.
import type { Schemas } from "../../../api/client";
import { getLocale, t, tMaybe } from "../../../i18n";

export type Webhook = Schemas["WebhookView"];
export type Delivery = Schemas["WebhookDeliveryView"];

export const webhookKeys = {
  all: ["webhooks"] as const,
  events: ["webhooks", "events"] as const,
  log: (id: number, status: string) => ["webhooks", "log", id, status] as const,
};

/** The docs site's page on webhooks: what a delivery carries and how a receiver checks its signature. */
export const docsPage = () => `https://miroshka000.github.io/mikan/${getLocale() === "en" ? "en/" : ""}operations/webhooks/`;

/** Why a delivery failed, in words, with the status the receiver answered when it did. */
export function failureText(code: string | undefined, status: number): string {
  const reason = code ? (tMaybe(`webhooks.failure.${code}`) ?? code) : "";
  return [reason, status > 0 ? t("webhooks.http", { code: status }) : ""].filter(Boolean).join(", ");
}

/** An event type in words; one this version has no text for (a later panel's) is its own name. */
export const eventLabel = (type: string) => tMaybe(`webhooks.events.${type}`) ?? type;

/** The sections of the picker of events, in the order they are shown. */
export const GROUPS = ["payments", "subscribers", "devices", "nodes", "other"] as const;
export type Group = (typeof GROUPS)[number];

const GROUP_OF: Record<string, Group> = { payment: "payments", balance: "payments", user: "subscribers", traffic: "subscribers", trial: "subscribers", device: "devices", node: "nodes" };

/** The event types as the picker lists them: by what they are about, each section in the API's order. */
export function groupedTypes(types: string[]): { group: Group; types: string[] }[] {
  return GROUPS.map((group) => ({ group, types: types.filter((x) => (GROUP_OF[x.split(".")[0]!] ?? "other") === group) })).filter((g) => g.types.length > 0);
}

/**
 * An address as the list shows it: the host and the first part of the path. A token often sits
 * further in (a Discord or Slack address), and a list is on screen where others may look.
 */
export function shortAddress(raw: string): string {
  try {
    const u = new URL(raw);
    const [first, ...rest] = u.pathname.split("/").filter(Boolean);
    const cut = rest.length > 0 || u.search !== "";
    return `${u.protocol}//${u.host}${first ? `/${first}${cut ? "/…" : ""}` : cut ? "/…" : ""}`;
  } catch {
    return raw;
  }
}

export type WebhookState = "paused" | "failing" | "failed" | "ok" | "idle";

/** How an address fares, from what is known: switched off, its last delivery being retried or given up, taken, or none yet. */
export function stateOf(w: Webhook): WebhookState {
  if (!w.enabled) return "paused";
  if (!w.last) return "idle";
  if (w.last.status === "retrying") return "failing";
  return w.last.status === "failed" ? "failed" : "ok";
}

export const STATE_TONE: Record<WebhookState, "ok" | "warn" | "bad" | "off"> = { paused: "off", failing: "bad", failed: "warn", ok: "ok", idle: "off" };

export const DELIVERY_TONE: Record<Delivery["status"], "ok" | "warn" | "bad"> = { delivered: "ok", retrying: "bad", failed: "warn" };
