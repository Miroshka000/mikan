import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban } from "lucide-react";
import { useState } from "react";
import { api, errorText, unwrap, type User } from "../../../api/client";
import { qk, useFilters, useTrafficDetail } from "../../../api/hooks";
import { useToast } from "../../../components/toast";
import { Bar, Button, Segmented, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { bytes } from "../../../lib/format";
import { Section } from "./section";

const PERIODS = [1, 7, 30] as const;

/**
 * Where the subscription goes: the bytes by site over a few days, summed on the nodes with
 * nothing else kept. Shown while the detail is on (Settings → Routing); a site may be
 * blocked for everyone from here, into the filters, and let through again the same way.
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
  const blocked = new Set(filters.data?.egress.enabled ? filters.data.egress.domains : []);
  // Blocking adds the site to the egress filter and turns the filter on; unblocking takes the
  // site out and leaves the filter's switch and its other entries as they are.
  const toggle = useMutation({
    mutationFn: ({ site, block }: { site: string; block: boolean }) => {
      const f = filters.data;
      if (!f) throw new Error("filters");
      const egress = block ? { ...f.egress, enabled: true, domains: [...new Set([...f.egress.domains, site])] } : { ...f.egress, domains: f.egress.domains.filter((d) => d !== site) };
      return unwrap(api.PATCH("/api/v1/filters", { body: { egress } }));
    },
    onSuccess: (_, { site, block }) => {
      void qc.invalidateQueries({ queryKey: qk.filters });
      toast.ok(t(block ? "userDrawer.sites.blocked" : "userDrawer.sites.unblocked", { site }));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const v = sites.data;
  // Whether the section is there is known before its skeleton: with the counting off it
  // shows only what was counted before, so it waits for the list instead of appearing and
  // going away.
  if (v ? !v.enabled && v.items.length === 0 : detail.data?.enabled !== true) return null;
  const top = v?.items[0] ? v.items[0].up + v.items[0].down : 0;
  return (
    <Section title={t("userDrawer.sites.title")} aside={v?.enabled === false ? t("userDrawer.sites.off") : undefined}>
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
            const isBlocked = blocked.has(s.site);
            const label = t(isBlocked ? "userDrawer.sites.unblock" : "userDrawer.sites.block", { site });
            return (
              <li key={s.site} className="flex items-center gap-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline justify-between gap-2 text-[13px]">
                    <span className="mono truncate">{site}</span>
                    <span className="num shrink-0 text-[var(--ink-600)]">{bytes(total)}</span>
                  </div>
                  <Bar pct={top ? (total / top) * 100 : 0} className="mt-1" />
                </div>
                {isDomain ? (
                  // One button for both ways: tinted while the site is in the filters, and its
                  // label says what a press does now (block it, or let everyone there again).
                  <Button
                    size="sm"
                    variant={isBlocked ? "danger" : "ghost"}
                    aria-label={label}
                    title={label}
                    loading={toggle.isPending && toggle.variables?.site === s.site}
                    disabled={toggle.isPending || !filters.data}
                    onClick={() => toggle.mutate({ site: s.site, block: !isBlocked })}
                  >
                    {toggle.isPending && toggle.variables?.site === s.site ? null : <Ban size={16} aria-hidden />}
                  </Button>
                ) : (
                  <span className="w-8 shrink-0" aria-hidden />
                )}
              </li>
            );
          })}
        </ul>
      )}
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("userDrawer.sites.hint", { n: v?.keep_days ?? 7 })}</p>
    </Section>
  );
}
