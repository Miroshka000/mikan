import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, useBlocker } from "@tanstack/react-router";
import { Eye, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../../api/client";
import { useExitDns, useNodes, useSubTemplates } from "../../../api/hooks";
import { Card, SaveBar } from "../../../components/layout";
import { Confirm, Drawer } from "../../../components/overlay";
import { SwitchRow } from "../../../components/switch";
import { Button, EmptyState, ErrorState, Field, Pill, Segmented, Skeleton } from "../../../components/ui";
import { t, useLocale } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { ExitDnsFields, exitDnsDraftOf, useSaveExitDns, type ExitDnsDraft } from "./exit-dns";
import { RulesEditor } from "./rules";
import { CoreBalance, tidyServers } from "./balance";
import { useSaveSettings } from "./shared";
import { Select } from "../../../components/select";

type Routes = Schemas["Routes"];
type DNS = NonNullable<Routes["dns"]>;
type List = NonNullable<Routes["lists"]>[number];

/** The parts of the routing, each in the section's menu. The own profiles are the subscription templates. */
export const ROUTING_PARTS = ["mode", "lists", "rules", "dns", "advanced"] as const;
export type RoutingPart = (typeof ROUTING_PARTS)[number];

// As many own lists as the server takes (subs.MaxLists).
const MAX_LISTS = 30;

const MODES = [
  { id: "ru_direct", title: "settings.routingRuDirect", sub: "settings.routingRuDirectSub" },
  { id: "all", title: "settings.routingAll", sub: "settings.routingAllSub" },
  { id: "blocked", title: "settings.routingBlocked", sub: "settings.routingBlockedSub" },
] as const;

// Where a service can go; "node:<id>" options follow from the nodes.
const TARGETS = [
  { id: "", label: "settings.routesFollow" },
  { id: "vpn", label: "settings.routesVpn" },
  { id: "direct", label: "settings.routesDirect" },
  { id: "block", label: "settings.routesBlock" },
] as const;

// Ready DNS sets for the device (GitHub issue #70): the panel's own is the empty one.
const DNS_PRESETS: { id: string; dns: DNS }[] = [
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

// What is empty is left out, as the server leaves it: a draft that was emptied again is not a change.
function tidy(r: Routes): Routes {
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
const dnsTextOf = (dns: DNS | undefined) => ({ nameserver: (dns?.nameserver ?? []).join("\n"), proxy: (dns?.proxy_server_nameserver ?? []).join("\n"), policy: policyText(dns?.policy) });

/** The refusal of the server groups' settings, the common ones' or a core's (routes_servers_xray);
 * every other one of sub_routes is the lists'. */
const serversRefused = (e: ApiError | null) => (e?.codes.sub_routes ?? "").startsWith("routes_servers");

/** The part a refusal of the server names, so a save from another part shows where it went wrong. */
function partOf(e: ApiError): RoutingPart | null {
  if (e.fields.sub_rules) return "rules";
  if (serversRefused(e)) return "advanced";
  if (e.fields.sub_routes) return "lists";
  return null;
}

/**
 * The routing of the subscriptions, a part at a time: the mode and the services, the own
 * lists and the apps past the tunnel, the own rules, DNS on the device and at the exit, the
 * server groups and fine tuning. One draft for all of it, saved from one bar; the profile a
 * client gets opens beside it on demand. A default Clash template takes its place for the
 * Clash apps, and the section says so.
 */
export function RoutingSection({ s, part, onPart }: { s: Schemas["SettingsView"]; part: RoutingPart; onPart: (p: RoutingPart) => void }) {
  const save = useSaveSettings();
  const exitQ = useExitDns();
  const saveExit = useSaveExitDns();
  const { draft, setDraft, dirty: routingDirty, reset } = useDraft({ mode: s.sub_routing, routes: s.sub_routes, rules: s.sub_rules });
  const exit = useDraft<ExitDnsDraft | null>(exitQ.data ? exitDnsDraftOf(exitQ.data) : null);
  const routes = draft.routes;
  const setRoutes = (f: (r: Routes) => Routes) => setDraft((d) => ({ ...d, routes: tidy(f(d.routes)) }));
  // The DNS fields keep their own text, so they are put back with the rest.
  const [dnsText, setDnsText] = useState(() => dnsTextOf(routes.dns));
  const [preview, setPreview] = useState(false);
  const dirty = routingDirty || exit.dirty;
  const discard = () => {
    reset();
    exit.reset();
    setDnsText(dnsTextOf(s.sub_routes.dns));
  };
  const submit = () => {
    if (routingDirty) {
      save.mutate(
        { sub_routing: draft.mode, sub_routes: draft.routes, sub_rules: draft.rules },
        {
          onError: (e) => {
            const p = e instanceof ApiError ? partOf(e) : null;
            if (p && p !== part) onPart(p);
          },
        },
      );
    }
    if (exit.dirty && exit.draft) {
      saveExit.mutate(exit.draft, { onError: () => part !== "dns" && onPart("dns") });
    }
  };
  const apiErr = save.error instanceof ApiError ? save.error : null;
  const errors = apiErr?.fields ?? {};
  const templates = useSubTemplates();
  const live = !!templates.data?.templates.some((x) => x.core === "mihomo" && x.default);
  // The settings show one tab at a time: leaving the routing (or the page) drops the draft,
  // so ask first. Its parts are one draft.
  const unsaved = dirty;
  const leave = useBlocker({
    shouldBlockFn: ({ current, next }) => unsaved && (current.pathname !== next.pathname || (next.search as { tab?: string }).tab !== "routing"),
    enableBeforeUnload: () => unsaved,
    withResolver: true,
  });

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">{live ? <Pill tone="warn">{t("settings.routesTemplateOn")}</Pill> : null}</div>
        <Button size="sm" onClick={() => setPreview(true)} aria-haspopup="dialog">
          <Eye size={16} aria-hidden /> {t("settings.routesShowProfile")}
        </Button>
      </div>
      {live ? (
        <div className="banner info flex-wrap">
          <span className="min-w-0 flex-1">{t("settings.routesTemplateLive")}</span>
          <Link to="/settings" search={{ tab: "subscription", part: "templates" }} className="btn btn-glass btn-sm">
            {t("settings.routesToTemplates")}
          </Link>
        </div>
      ) : null}

      {part === "mode" ? <ModePart mode={draft.mode} setMode={(mode) => setDraft((d) => ({ ...d, mode }))} routes={routes} setRoutes={setRoutes} /> : null}
      {part === "lists" ? <ListsPart routes={routes} setRoutes={setRoutes} error={serversRefused(apiErr) ? undefined : errors.sub_routes} /> : null}
      {part === "rules" ? (
        <Card title={t("settings.rules")} sub={t("settings.routingPartsSub.rules")}>
          <RulesEditor
            value={draft.rules}
            onChange={(f) => setDraft((d) => ({ ...d, rules: f(d.rules) }))}
            error={errors.sub_rules}
            badLine={errors.sub_rules && typeof apiErr?.values.sub_rules === "number" ? apiErr.values.sub_rules : 0}
            targets={s.rule_targets}
          />
        </Card>
      ) : null}
      {part === "dns" ? (
        <>
          <Card title={t("settings.routesDnsDevice")} sub={t("settings.routesDnsDeviceSub")}>
            <DeviceDns
              text={dnsText}
              onPreset={(dns) => {
                setRoutes((r) => ({ ...r, dns }));
                setDnsText(dnsTextOf(dns));
              }}
              onText={(next) => {
                setDnsText(next);
                setRoutes((r) => ({ ...r, dns: { nameserver: lines(next.nameserver), proxy_server_nameserver: lines(next.proxy), policy: parsePolicy(next.policy) } }));
              }}
            />
          </Card>
          <Card title={t("settings.exitDns.title")} sub={t("settings.exitDns.lead")} i={2}>
            {exitQ.isError ? <ErrorState text={errorText(exitQ.error)} onRetry={() => void exitQ.refetch()} /> : null}
            {exitQ.data && exit.draft ? (
              <ExitDnsFields v={exitQ.data} draft={exit.draft} setDraft={(f) => exit.setDraft((d) => (d ? f(d) : d))} error={saveExit.error} />
            ) : exitQ.isPending ? (
              <Skeleton className="h-32 w-full rounded-2xl" />
            ) : null}
          </Card>
        </>
      ) : null}
      {part === "advanced" ? (
        <AdvancedPart routes={routes} setRoutes={setRoutes} error={serversRefused(apiErr) ? errors.sub_routes : undefined} ofCore={apiErr?.codes.sub_routes !== "routes_servers"} />
      ) : null}
      <p className="px-1 text-xs text-[var(--ink-500)]">{t("settings.routesNote")}</p>

      <SaveBar dirty={dirty} saving={save.isPending || saveExit.isPending} onSave={submit} onReset={discard} />

      <Drawer open={preview} onOpenChange={setPreview} title={t("settings.routesPreview")}>
        <div className="pt-4">
          {preview ? <PreviewCard mode={draft.mode} routes={draft.routes} rules={draft.rules} /> : null}
        </div>
      </Drawer>
      <Confirm
        open={leave.status === "blocked"}
        onOpenChange={(open) => !open && leave.reset?.()}
        title={t("settings.page.leaveTitle")}
        text={t("settings.routesLeaveText")}
        confirm={t("settings.page.leaveConfirm")}
        danger
        onConfirm={() => leave.proceed?.()}
      />
    </div>
  );
}

/* ------------------------------------------------------------------ parts */

type SetRoutes = (f: (r: Routes) => Routes) => void;

function useTargetSelect() {
  const nodes = useNodes();
  const nodeName = (n: { id: number; name: string; local: boolean }) => t("settings.routesOnServer", { name: n.name || (n.local ? t("settings.routesThisServer") : `#${n.id}`) });
  return (id: string, value: string, onChange: (v: string) => void, opts: { follow?: boolean; label?: string; className?: string } = {}) => (
    <Select id={id} className={opts.className ?? "input max-w-[220px]"} value={value} onChange={(e) => onChange(e.target.value)} aria-label={opts.label}>
      {TARGETS.filter((x) => opts.follow !== false || x.id).map((x) => (
        <option key={x.id} value={x.id}>
          {t(x.label)}
        </option>
      ))}
      {(nodes.data ?? []).length ? (
        <optgroup label={t("settings.routesServer")}>
          {(nodes.data ?? []).map((n) => (
            <option key={n.id} value={`node:${n.id}`}>
              {nodeName(n)}
            </option>
          ))}
        </optgroup>
      ) : null}
    </Select>
  );
}

function useCatalog() {
  return useQuery({ queryKey: ["routes-catalog"], queryFn: ({ signal }) => unwrap(api.GET("/api/v1/settings/routes/catalog", { signal })), staleTime: Infinity });
}

/** The mode, then each service as a tile with where it goes; the ones set apart stand out. */
function ModePart({ mode, setMode, routes, setRoutes }: { mode: Schemas["SettingsView"]["sub_routing"]; setMode: (m: Schemas["SettingsView"]["sub_routing"]) => void; routes: Routes; setRoutes: SetRoutes }) {
  const catalog = useCatalog();
  const locale = useLocale();
  const targetSelect = useTargetSelect();
  const [q, setQ] = useState("");
  const services = catalog.data?.services ?? [];
  const nameOf = (svc: (typeof services)[number]) => (locale === "en" ? svc.name_en : svc.name);
  const shown = services.filter((svc) => !q.trim() || nameOf(svc).toLowerCase().includes(q.trim().toLowerCase()) || svc.id.includes(q.trim().toLowerCase()));
  const set = Object.keys(routes.services ?? {}).length;
  const setService = (id: string, target: string) =>
    setRoutes((r) => {
      const next = { ...(r.services ?? {}) };
      if (target) next[id] = target;
      else delete next[id];
      return { ...r, services: next };
    });
  return (
    <>
      <Card title={t("settings.routing")} sub={t("settings.routingHint")}>
        <div className="grid gap-2 sm:grid-cols-3" role="radiogroup" aria-label={t("settings.routing")}>
          {MODES.map((m) => (
            <button key={m.id} type="button" role="radio" aria-checked={mode === m.id} className="opt" onClick={() => setMode(m.id)}>
              <span className="font-semibold">{t(m.title)}</span>
              <span className="text-xs text-[var(--ink-500)]">{t(m.sub)}</span>
            </button>
          ))}
        </div>
      </Card>
      <Card title={t("settings.routesServices")} sub={t("settings.routesServicesHint")} i={2} aside={set ? <Pill tone="off">{t("settings.routesChanged", { n: set })}</Pill> : null}>
        <div className="mb-3 flex items-center gap-2">
          <div className="relative w-full max-w-[320px]">
            <Search size={16} className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-[var(--ink-400)]" aria-hidden />
            <input className="input w-full pl-9" type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("settings.routesSearch")} aria-label={t("settings.routesSearch")} />
          </div>
        </div>
        {catalog.isError ? <ErrorState text={errorText(catalog.error)} onRetry={() => void catalog.refetch()} /> : null}
        {catalog.isPending ? (
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3" aria-busy="true">
            {[0, 1, 2, 3, 4, 5].map((i) => (
              <Skeleton key={i} className="h-[88px] w-full rounded-2xl" />
            ))}
          </div>
        ) : null}
        {catalog.data && !shown.length ? <EmptyState icon={Search} search title={t("settings.routesSearchEmpty")} text={t("settings.routesSearchEmptyText")} /> : null}
        <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
          {shown.map((svc) => {
            const id = `s-svc-${svc.id}`;
            const value = routes.services?.[svc.id] ?? "";
            return (
              <div key={svc.id} className="panel-soft flex flex-col gap-2 p-3" style={value ? { borderColor: "var(--mikan-400)" } : undefined}>
                <label htmlFor={id} className="flex min-w-0 items-center gap-2 text-sm font-medium text-[var(--ink-900)]">
                  <span className="text-lg leading-none" aria-hidden>
                    {svc.icon}
                  </span>
                  <span className="truncate">{nameOf(svc)}</span>
                </label>
                {targetSelect(id, value, (v) => setService(svc.id, v), { className: "input h-9 w-full text-[13px]" })}
              </div>
            );
          })}
        </div>
      </Card>
    </>
  );
}

/** The own rule lists by link, and the apps that go past the tunnel. */
function ListsPart({ routes, setRoutes, error }: { routes: Routes; setRoutes: SetRoutes; error?: string }) {
  const catalog = useCatalog();
  const locale = useLocale();
  const targetSelect = useTargetSelect();
  const setList = (i: number, patch: Partial<List>) => setRoutes((r) => ({ ...r, lists: (r.lists ?? []).map((l, j) => (j === i ? { ...l, ...patch } : l)) }));
  const addList = () => setRoutes((r) => ({ ...r, lists: [...(r.lists ?? []), { name: `list-${(r.lists?.length ?? 0) + 1}`, url: "", behavior: "classical", format: "yaml", target: "vpn" }] }));
  const dropList = (i: number) => setRoutes((r) => ({ ...r, lists: (r.lists ?? []).filter((_, j) => j !== i) }));
  const toggleDirect = (id: string) =>
    setRoutes((r) => {
      const has = (r.direct ?? []).includes(id);
      return { ...r, direct: has ? (r.direct ?? []).filter((x) => x !== id) : [...(r.direct ?? []), id] };
    });
  const lists = routes.lists ?? [];
  return (
    <>
      <Card title={t("settings.routesLists")} sub={t("settings.routesListsHint")}>
        <div className="flex flex-col gap-3">
          {lists.map((l, i) => (
            <div key={i} className="panel-soft grid gap-2 p-3 sm:grid-cols-[160px_minmax(0,1fr)]">
              <input className="input mono" value={l.name} onChange={(e) => setList(i, { name: e.target.value.toLowerCase() })} maxLength={32} aria-label={t("settings.routesListName")} placeholder="whitelist" />
              <input className="input mono" value={l.url} onChange={(e) => setList(i, { url: e.target.value.trim() })} maxLength={500} inputMode="url" autoComplete="off" spellCheck={false} aria-label={t("settings.routesListUrl")} placeholder="https://raw.githubusercontent.com/…/list.yaml" />
              <div className="flex flex-wrap items-center gap-2 sm:col-span-2">
                <Select className="input max-w-[170px]" value={l.behavior} onChange={(e) => setList(i, { behavior: e.target.value })} aria-label={t("settings.routesListBehavior")}>
                  <option value="classical">{t("settings.routesListClassical")}</option>
                  <option value="domain">{t("settings.routesListDomain")}</option>
                  <option value="ipcidr">{t("settings.routesListIP")}</option>
                </Select>
                <Select className="input max-w-[110px]" value={l.format} onChange={(e) => setList(i, { format: e.target.value })} aria-label={t("settings.routesListFormat")}>
                  <option value="yaml">YAML</option>
                  <option value="text">TXT</option>
                  <option value="mrs">MRS</option>
                </Select>
                {targetSelect(`s-list-${i}`, l.target, (v) => setList(i, { target: v }), { follow: false, label: t("settings.routesListTarget") })}
                <Button variant="ghost" className="ml-auto" onClick={() => dropList(i)} aria-label={t("settings.routesListRemoveNamed", { name: l.name })}>
                  {t("settings.routesListRemove")}
                </Button>
              </div>
            </div>
          ))}
          {!lists.length ? <p className="text-[13px] text-[var(--ink-500)]">{t("settings.routesListsEmpty")}</p> : null}
          <div>
            <Button variant="ghost" onClick={addList} disabled={lists.length >= MAX_LISTS}>
              + {t("settings.routesListAdd")}
            </Button>
          </div>
          {error ? (
            <p className="text-xs text-[var(--berry-600)]" role="alert">
              {error}
            </p>
          ) : null}
        </div>
      </Card>
      <Card title={t("settings.routesApps")} sub={t("settings.routesAppsHint")} i={2}>
        {catalog.isError ? <ErrorState text={errorText(catalog.error)} onRetry={() => void catalog.refetch()} /> : null}
        {catalog.isPending ? <Skeleton className="h-40 w-full rounded-2xl" /> : null}
        <div className="grid gap-2 sm:grid-cols-2">
          {(catalog.data?.direct ?? []).map((d) => {
            const on = (routes.direct ?? []).includes(d.id);
            return (
              <label key={d.id} className="panel-soft flex cursor-pointer items-start gap-3 p-3 text-sm" style={on ? { borderColor: "var(--mikan-400)" } : undefined}>
                <input type="checkbox" className="check mt-0.5 shrink-0" checked={on} onChange={() => toggleDirect(d.id)} />
                <span>{locale === "en" ? d.name_en : d.name}</span>
              </label>
            );
          })}
        </div>
      </Card>
    </>
  );
}

/** The DNS of the device's Clash profile: a ready set or the servers as mihomo writes them. */
function DeviceDns({ text, onPreset, onText }: { text: ReturnType<typeof dnsTextOf>; onPreset: (dns: DNS) => void; onText: (t: ReturnType<typeof dnsTextOf>) => void }) {
  const edit = (k: keyof typeof text) => (e: React.ChangeEvent<HTMLTextAreaElement>) => onText({ ...text, [k]: e.target.value });
  return (
    <>
      <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label={t("settings.routesDnsPresets")}>
        {DNS_PRESETS.map((p) => (
          <button key={p.id} type="button" className="chip-btn" onClick={() => onPreset(p.dns)}>
            {t(`settings.routesDnsPreset.${p.id}` as "settings.routesDnsPreset.panel")}
          </button>
        ))}
      </div>
      <Field label={t("settings.routesDnsServers")} htmlFor="s-dns-ns" hint={t("settings.routesDnsHint")}>
        <textarea id="s-dns-ns" className="input mono min-h-[72px]" value={text.nameserver} onChange={edit("nameserver")} spellCheck={false} placeholder="https://1.1.1.1/dns-query#PROXY" />
      </Field>
      <Field label={t("settings.routesDnsProxy")} htmlFor="s-dns-proxy" hint={t("settings.routesDnsProxyHint")}>
        <textarea id="s-dns-proxy" className="input mono min-h-[56px]" value={text.proxy} onChange={edit("proxy")} spellCheck={false} placeholder="1.1.1.1" />
      </Field>
      <Field label={t("settings.routesDnsPolicy")} htmlFor="s-dns-policy" hint={t("settings.routesDnsPolicyHint")}>
        <textarea id="s-dns-policy" className="input mono min-h-[72px]" value={text.policy} onChange={edit("policy")} spellCheck={false} placeholder="+.cn: https://dns.alidns.com/dns-query" />
      </Field>
    </>
  );
}

/** How the server groups look in the apps, each core's own balance, and the switches for
 * experts. error is a refusal of the servers' settings, ofCore of a core's own. */
function AdvancedPart({ routes, setRoutes, error, ofCore }: { routes: Routes; setRoutes: SetRoutes; error?: string; ofCore: boolean }) {
  const servers = routes.servers ?? {};
  const setServers = (patch: Partial<NonNullable<Routes["servers"]>>) => setRoutes((r) => ({ ...r, servers: { ...(r.servers ?? {}), ...patch } }));
  const tune = routes.tune ?? {};
  const setTune = (patch: Partial<NonNullable<Routes["tune"]>>) => setRoutes((r) => ({ ...r, tune: { ...(r.tune ?? {}), ...patch } }));
  return (
    <>
      <Card title={t("settings.routesServers")} sub={t("settings.routesServersSub")}>
        <Field label={t("settings.routesAutoKind")} hint={t(servers.auto === "fallback" ? "settings.routesAutoFallbackHint" : "settings.routesAutoFastestHint")}>
          <Segmented
            label={t("settings.routesAutoKind")}
            value={servers.auto === "fallback" ? "fallback" : "fastest"}
            onChange={(v) => setServers({ auto: v === "fallback" ? "fallback" : undefined })}
            options={[
              { value: "fastest", label: t("settings.routesAutoFastest") },
              { value: "fallback", label: t("settings.routesAutoFallback") },
            ]}
          />
        </Field>
        <Field label={t("settings.routesInterval")} htmlFor="s-interval" hint={t("settings.routesIntervalHint")}>
          <Select id="s-interval" className="input max-w-[280px]" value={servers.interval ?? 0} onChange={(e) => setServers({ interval: Number(e.target.value) || undefined })}>
            {[...new Set([0, 60, 120, 600, 1800, servers.interval ?? 0])].map((n) => (
              <option key={n} value={n}>
                {n ? t("settings.routesIntervalSec", { n }) : t("settings.routesIntervalDefault")}
              </option>
            ))}
          </Select>
        </Field>
        <SwitchRow label={t("settings.routesCountries")} sub={t("settings.routesCountriesSub")} checked={!servers.no_countries} onChange={(v) => setServers({ no_countries: v ? undefined : true })} />
        {error && !ofCore ? (
          <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
            {error}
          </p>
        ) : null}
      </Card>
      <CoreBalance servers={servers} setServers={setServers} error={ofCore ? error : undefined} />
      <Card title={t("settings.routesTune")} sub={t("common.forExperts")} i={3}>
        <SwitchRow label={t("settings.routesQuic")} sub={t("settings.routesQuicSub")} checked={!!tune.block_quic} onChange={(v) => setTune({ block_quic: v || undefined })} />
        <SwitchRow label={t("settings.routesSniffer")} sub={t("settings.routesSnifferSub")} checked={!!tune.sniffer} onChange={(v) => setTune({ sniffer: v || undefined })} />
        <SwitchRow label={t("settings.routesRealIP")} sub={t("settings.routesRealIPSub")} checked={!!tune.real_ip} onChange={(v) => setTune({ real_ip: v || undefined })} />
      </Card>
    </>
  );
}

// The built-in profile a user with every connection gets, before anything is saved: the
// unsaved rules too.
function PreviewCard({ mode, routes, rules }: { mode: Schemas["SettingsView"]["sub_routing"]; routes: Routes; rules: string }) {
  const preview = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/settings/routes/preview", { body: { sub_routing: mode, sub_routes: routes, sub_rules: rules } })),
  });
  // Shown at once when the panel opens; the button asks again after more changes.
  const { mutate } = preview;
  useEffect(() => mutate(), [mutate]);
  return (
    <section>
      <p className="mb-3 text-[13px] text-[var(--ink-500)]">{t("settings.routesPreviewSub")}</p>
      <Button variant="ghost" loading={preview.isPending} onClick={() => preview.mutate()}>
        {preview.data ? t("settings.routesPreviewAgain") : t("settings.routesPreviewShow")}
      </Button>
      {preview.error ? (
        <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
          {errorText(preview.error)}
        </p>
      ) : null}
      {preview.data?.notes.length ? <PreviewNotes notes={preview.data.notes} /> : null}
      {preview.data ? <pre className="panel-soft mono mt-3 max-h-[70vh] overflow-auto p-3 text-[12px] leading-[1.45]">{preview.data.profile}</pre> : null}
    </section>
  );
}

const CORE_NAMES: Record<Schemas["Note"]["core"], string> = { mihomo: "Clash", singbox: "sing-box", xray: "Xray" };

/** What the sing-box and Xray apps make of the profile: the nearest kind of a group they have none of, and what they leave out. */
function PreviewNotes({ notes }: { notes: Schemas["Note"][] }) {
  return (
    <section className="mt-4" aria-labelledby="routes-notes">
      <h3 id="routes-notes" className="text-[13px] font-semibold text-[var(--ink-700)]">
        {t("settings.routesNotesTitle")}
      </h3>
      <p className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.routesNotesSub")}</p>
      <ul className="mt-2 flex flex-col gap-2">
        {notes.map((n) => (
          <li key={`${n.core}/${n.group}/${n.code}`} className="panel-soft flex flex-wrap gap-x-2 gap-y-1 p-3 text-[13px]">
            <span className="font-medium">{CORE_NAMES[n.core]}</span>
            <span className="min-w-0 break-words text-[var(--ink-700)]">{t(`settings.routesNotes.${n.code}`, { group: n.group })}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
