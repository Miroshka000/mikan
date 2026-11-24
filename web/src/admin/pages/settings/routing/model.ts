import type { Schemas } from "../../../../api/client";
import { tidyServers } from "./balance";

/** The routing of the built-in profile, the settings' or a simple template's: what the parts edit. */
export type Routes = Schemas["Routes"];
export type DNS = NonNullable<Routes["dns"]>;
export type List = NonNullable<Routes["lists"]>[number];
export type Mode = Schemas["SettingsView"]["sub_routing"];
export type SetRoutes = (f: (r: Routes) => Routes) => void;

/** The parts of the routing, each in the section's menu. The own profiles are the subscription templates. */
export const ROUTING_PARTS = ["mode", "lists", "rules", "dns", "advanced"] as const;
export type RoutingPart = (typeof ROUTING_PARTS)[number];

// As many own lists as the server takes (routing.MaxLists).
export const MAX_LISTS = 30;

export const MODES = [
  { id: "ru_direct", title: "settings.routingRuDirect", sub: "settings.routingRuDirectSub" },
  { id: "all", title: "settings.routingAll", sub: "settings.routingAllSub" },
  { id: "blocked", title: "settings.routingBlocked", sub: "settings.routingBlockedSub" },
] as const;

// Where a service or a list can go; "node:<id>" ones follow from the nodes (useTargets).
export const TARGETS = [
  { id: "", label: "settings.routesFollow" },
  { id: "vpn", label: "settings.routesVpn" },
  { id: "direct", label: "settings.routesDirect" },
  { id: "block", label: "settings.routesBlock" },
] as const;

// Ready DNS sets for the device (GitHub issue #70): the panel's own is the empty one.
export const DNS_PRESETS: { id: string; dns: DNS }[] = [
  { id: "panel", dns: {} },
  { id: "tunnel", dns: { nameserver: ["https://1.1.1.1/dns-query#PROXY", "https://8.8.8.8/dns-query#PROXY"], proxy_server_nameserver: ["1.1.1.1", "8.8.8.8"] } },
  {
    id: "ruYandex",
    dns: {
      nameserver: ["https://1.1.1.1/dns-query#PROXY", "https://8.8.8.8/dns-query#PROXY"],
      proxy_server_nameserver: ["77.88.8.8", "1.1.1.1"],
      policy: [
        { match: "+.ru", servers: ["https://77.88.8.8/dns-query"] },
        { match: "+.su", servers: ["https://77.88.8.8/dns-query"] },
        { match: "+.xn--p1ai", servers: ["https://77.88.8.8/dns-query"] },
      ],
    },
  },
];

/** Whether the code of a refusal of the routes is the server groups' (the common ones' or a
 * core's, routes_servers_xray): every other one is the lists'. */
export const serversRefused = (code: string | undefined) => (code ?? "").startsWith("routes_servers");

// What is empty is left out, as the server leaves it: a draft that was emptied again is not a change.
export function tidy(r: Routes): Routes {
  const out = { ...r };
  if (!Object.keys(out.services ?? {}).length) delete out.services;
  if (!out.direct?.length) delete out.direct;
  if (!out.lists?.length) delete out.lists;
  if (out.servers) out.servers = tidyServers(out.servers);
  const s = out.servers;
  if (!s?.auto && !s?.interval && !s?.no_countries && !s?.mihomo && !s?.singbox && !s?.xray) delete out.servers;
  if (!out.tune?.block_quic && !out.tune?.sniffer && !out.tune?.real_ip) delete out.tune;
  if (!out.dns?.nameserver?.length && !out.dns?.proxy_server_nameserver?.length && !out.dns?.policy?.length) delete out.dns;
  return out;
}

const lines = (s: string) =>
  s
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);

// "+.cn: https://dns.alidns.com/dns-query, 223.5.5.5" per line.
function parsePolicy(text: string): NonNullable<DNS["policy"]> {
  return lines(text).map((l) => {
    const [match = "", rest = ""] = l.split(/:\s+/, 2);
    return { match: match.trim(), servers: rest.split(",").map((x) => x.trim()).filter(Boolean) };
  });
}
const policyText = (p: DNS["policy"]) => (p ?? []).map((x) => `${x.match}: ${x.servers.join(", ")}`).join("\n");

/** The DNS of the device as the fields show it: a server a line, a policy a line. */
export type DnsText = { nameserver: string; proxy: string; policy: string };
export const dnsTextOf = (dns: DNS | undefined): DnsText => ({ nameserver: (dns?.nameserver ?? []).join("\n"), proxy: (dns?.proxy_server_nameserver ?? []).join("\n"), policy: policyText(dns?.policy) });
/** The DNS the fields' text stands for. */
export const dnsOfText = (text: DnsText): DNS => ({ nameserver: lines(text.nameserver), proxy_server_nameserver: lines(text.proxy), policy: parsePolicy(text.policy) });
