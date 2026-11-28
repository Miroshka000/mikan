import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import * as Menu from "@radix-ui/react-dropdown-menu";
import { Ellipsis } from "lucide-react";
import { useState } from "react";
import { api, errorText, unwrap, type Schemas, type User } from "../../../api/client";
import { qk, useFilters, useTrafficDetail } from "../../../api/hooks";
import { useToast } from "../../../components/toast";
import { Bar, Button, Segmented, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { bytes } from "../../../lib/format";
import { Section } from "./section";

type SiteAction = Schemas["PutSiteFilterInputBody"]["action"];

const PERIODS = [1, 7, 30] as const;

/**
 * Where the subscription goes: the bytes by site over a few days, summed on the nodes with
 * nothing else kept. Individual domain rules can also be managed here.
 */
export function SitesSection({ u }: { u: User }) {
  const [days, setDays] = useState<(typeof PERIODS)[number]>(7);
  const sites = useQuery({
    queryKey: ["user-sites", u.id, days],
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/users/{id}/sites", { params: { path: { id: u.id }, query: { days } }, signal })),
  });
  const filters = useFilters();
  const detail = useTrafficDetail();
  const qc = useQueryClient();
  const toast = useToast();
  const setFilter = useMutation({
    mutationFn: ({ site, action }: { site: string; action: SiteAction }) =>
      unwrap(api.PUT("/api/v1/filters/site", { body: { user_id: u.id, domain: site, action } })),
    onSuccess: (data) => {
      qc.setQueryData(qk.filters, data);
      toast.ok(t("filters.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });

  const v = sites.data;
  const egress = filters.data?.egress;
  const personal = (filters.data?.sites ?? []).filter((site) => site.user_id === u.id);
  const scoped = (egress?.rules ?? []).filter((r) => {
    const selected = r.users?.includes(u.id)
      || (u.folder_id !== null && r.folders?.includes(u.folder_id))
      || (u.tariff_id !== null && r.tariffs?.includes(u.tariff_id));
    return r.scope === "only" ? selected : !selected;
  });
  const hasRules = Boolean(personal.length || scoped.length || egress?.ports.length
    || egress?.networks.length || egress?.domains.length || (egress?.enabled && egress?.mail));
  const showTraffic = v ? v.enabled || v.items.length > 0 : detail.data?.enabled === true;
  if (!showTraffic && !hasRules) return null;
  const top = v?.items[0] ? v.items[0].up + v.items[0].down : 0;
  return (
    <Section title={t("userDrawer.sites.title")} aside={v?.enabled === false ? t("userDrawer.sites.off") : undefined}>
      {hasRules && egress && (
        <details className="panel-soft mb-3 p-3">
          <summary className="cursor-pointer text-sm">{t("userDrawer.sites.rules")}</summary>
          {!egress.enabled && <p className="mt-2 text-xs">{t("userDrawer.sites.rulesOff")}</p>}
          <ul className="mt-2 space-y-1 text-xs">
            {egress.ports.map((value) => <FilterItem key={`port-${value}`} kind="port" value={value} />)}
            {egress.mail && filters.data?.mail_ports.map((value) => <FilterItem key={`mail-${value}`} kind="port" value={value} />)}
            {egress.networks.map((value) => <FilterItem key={`network-${value}`} kind="network" value={value} />)}
            {egress.domains.map((value) => <FilterItem key={`domain-${value}`} kind="domain" value={value} />)}
            {scoped.map((rule, i) => <FilterItem key={`scoped-${i}`} kind={rule.kind} value={rule.value} />)}
            {personal.map((rule) => (
              <li className="flex flex-wrap items-center gap-2" key={rule.domain}>
                <span>{t(rule.allow ? "userDrawer.sites.allowClient" : "userDrawer.sites.blockClient", { site: rule.domain })}</span>
                <Button size="sm" variant="ghost" disabled={setFilter.isPending} onClick={() => setFilter.mutate({ site: rule.domain, action: "clear" })}>
                  {t("filters.rules.remove")}
                </Button>
              </li>
            ))}
          </ul>
        </details>
      )}
      {showTraffic && (
        <>
          <div className="mb-3">
            <Segmented label={t("userDrawer.sites.period")} value={String(days)} onChange={(x) => setDays(Number(x) as (typeof PERIODS)[number])} options={PERIODS.map((d) => ({ value: String(d), label: t("userDrawer.sites.days", { n: d }) }))} />
          </div>
          {sites.isPending ? (
            <div className="flex flex-col gap-2">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} style={{ height: 36, borderRadius: 10 }} />
              ))}
            </div>
          ) : sites.isError ? (
            <div className="banner err" role="alert">
              {errorText(sites.error)}
            </div>
          ) : !v || v.items.length === 0 ? (
            <p className="text-xs text-[var(--ink-500)]">{t("userDrawer.sites.none")}</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {v.items.map((s) => {
                const total = s.up + s.down;
                const site = s.site === "*" ? t("userDrawer.sites.other") : s.site;
                const isDomain = s.site !== "*" && /[a-z]/i.test(s.site) && !s.site.includes(":");
                const override = personal.find((rule) => rule.domain === s.site);
                return (
                  <li key={s.site} className="flex items-center gap-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-baseline justify-between gap-2 text-[13px]">
                        <span className="mono truncate">{site}</span>
                        <span className="num shrink-0 text-[var(--ink-600)]">{bytes(total)}</span>
                      </div>
                      <Bar pct={top ? (total / top) * 100 : 0} className="mt-1" />
                    </div>
                    {isDomain && (
                      <Menu.Root>
                        <Menu.Trigger asChild>
                          <Button size="sm" variant="ghost" aria-label={t("userDrawer.sites.actions", { site })} disabled={setFilter.isPending || !filters.data}>
                            <Ellipsis size={16} aria-hidden />
                          </Button>
                        </Menu.Trigger>
                        <Menu.Portal>
                          <Menu.Content className="menu glass-strong" align="end" sideOffset={6}>
                            <Menu.Item className="menu-item" onSelect={() => setFilter.mutate({ site: s.site, action: "block-all" })}>
                              {t("userDrawer.sites.block", { site })}
                            </Menu.Item>
                            <Menu.Item className="menu-item" disabled={override?.allow === false} onSelect={() => setFilter.mutate({ site: s.site, action: "block" })}>
                              {t("userDrawer.sites.blockClient", { site })}
                            </Menu.Item>
                            <Menu.Item className="menu-item" disabled={override?.allow === true} onSelect={() => setFilter.mutate({ site: s.site, action: "allow" })}>
                              {t("userDrawer.sites.allowClient", { site })}
                            </Menu.Item>
                          </Menu.Content>
                        </Menu.Portal>
                      </Menu.Root>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
          <p className="mt-3 text-xs text-[var(--ink-500)]">{t("userDrawer.sites.hint", { n: v?.keep_days ?? 7 })}</p>
        </>
      )}
    </Section>
  );
}

function FilterItem({ kind, value }: { kind: "port" | "network" | "domain"; value: string }) {
  return <li>{t(`filters.rules.${kind}`)} <span className="mono break-all">{value}</span></li>;
}
