// What a client's app gets of its subscription (#290), as the panel's preview tells it: the
// request the admin writes and what the answer holds.
import type { Schemas } from "../../../../api/client";

export type Preview = Schemas["SubPreview"];
export type PreviewLeft = Schemas["SubPreviewLeft"];
export type PreviewRequest = Schemas["SubPreviewInputBody"];
export type PreviewFormat = NonNullable<PreviewRequest["format"]>;

/** The formats a link may name (?format=), "" for none: the headers decide. */
export const PREVIEW_FORMATS = ["", "clash", "singbox", "xray", "uri", "html"] as const;

/** The reasons the panel leaves a server out, in the order the card lists them: why the client
 * does not have it at all first, then why this request does not get it. */
export const LEFT_ORDER: PreviewLeft["reason"][] = [
  "not_own",
  "not_in_group",
  "not_in_tariff",
  "pool_shut",
  "inbound_off",
  "node_off",
  "balance",
  "hidden",
  "host_state",
  "host_trial",
  "host_group",
  "app",
  "broken",
];

/** The tone an answer is shown in: the servers went out, a stub, or nothing the app can use. */
export const OUTCOME_TONE = { served: "ok", stub: "warn", blocked: "bad", dropped: "bad", error: "bad" } as const satisfies Record<Preview["outcome"], "ok" | "warn" | "bad">;

/** Where the client's servers come from: the card's own list of protocols (stronger than
 * anything), the group's own servers, the tariff's, or all of the panel without a tariff. */
export type ServersFrom = "own" | "group" | "tariff" | "all";

export function serversFrom(p: Pick<Preview, "own" | "by_group">, tariff: number | null | undefined): ServersFrom {
  if (p.own) return "own";
  if (p.by_group) return "group";
  return tariff ? "tariff" : "all";
}

/** Whether the answer is a profile an own template can make: Clash, sing-box or Xray. */
export function madeByTemplate(p: Pick<Preview, "outcome" | "format">): boolean {
  return p.outcome === "served" && (p.format === "clash" || p.format === "singbox" || p.format === "xray");
}

/** The left out grouped by reason, in LEFT_ORDER, only the reasons there are. */
export function leftByReason(left: PreviewLeft[]): [PreviewLeft["reason"], PreviewLeft[]][] {
  const out: [PreviewLeft["reason"], PreviewLeft[]][] = [];
  for (const reason of LEFT_ORDER) {
    const list = left.filter((l) => l.reason === reason);
    if (list.length) out.push([reason, list]);
  }
  return out;
}

/** The distinct User-Agents of a client's latest requests, newest first, at most n: the apps
 * the client really uses, one tap each. */
export function recentAgents(items: { agent: string }[], n: number): string[] {
  const out: string[] = [];
  for (const it of items) {
    const a = it.agent.trim();
    if (a && !out.includes(a)) out.push(a);
    if (out.length === n) break;
  }
  return out;
}

/** The file a format's body is saved as: its extension and media type. */
export function bodyFile(format: string): { ext: string; type: string } {
  switch (format) {
    case "clash":
      return { ext: "yaml", type: "text/yaml" };
    case "singbox":
    case "xray":
      return { ext: "json", type: "application/json" };
    default:
      return { ext: "txt", type: "text/plain" };
  }
}
