import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Archive, CalendarClock, Layers, Package, Pencil, Plus, RefreshCcw, Tag } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api, errorText, unwrap, type Tariff } from "../../api/client";
import { qk, useInbounds, useNodes, usePackages, usePaymentSettings, usePools, useTariffs } from "../../api/hooks";
import { Confirm } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { Tabs } from "../../components/tabs";
import { useToast } from "../../components/toast";
import { Button, EmptyState, PageHeader, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { bytes, days, months, rubles, termMonths } from "../../lib/format";
import { nodeLabel } from "../../lib/node-label";
import { TARIFF_TABS } from "../search";
import { LifecycleDrawer } from "./lifecycle";
import { PackagesCard } from "./packages";
import { modeShort, PoolsCard } from "./pools";
import { TariffDrawer } from "./tariffs/drawer";
import { modeOf } from "./user/device-mode";

/** How long a term on the tariff runs: days, or months up to the billing day. */
function tariffTerm(tr: Tariff): string {
  if (!tr.duration_days) return t("time.forever");
  if (tr.billing_day != null) return t("tariffs.termToDay", { months: months(termMonths(tr.duration_days)), d: tr.billing_day });
  return days(tr.duration_days);
}

function resetLabel(tr: Tariff): string {
  if (tr.reset_strategy === "month_start" && tr.billing_day != null) return t("tariffs.resetOnDay", { d: tr.billing_day });
  return t(`tariffs.resetLabel.${tr.reset_strategy}`);
}

export function tariffSummary(tr: Tariff): string {
  const parts = [
    tr.traffic_limit != null ? bytes(tr.traffic_limit) : t("users.unlimited"),
    tariffTerm(tr),
    tr.device_limit != null ? t(modeOf(tr) === "online" ? "deviceMode.shortOnline" : "userDrawer.devicesShort", { n: tr.device_limit }) : t("tariffs.devicesUnlimitedShort"),
    ...(tr.speed_limit != null ? [t("tariffs.speedShort", { n: tr.speed_limit })] : []),
  ];
  return parts.join(" · ");
}

const TAB_ICONS = { tariffs: Tag, pools: Layers, packages: Package } as const;

/** A tariff's groups apart from the main traffic, by name: "WL 100 GB · Premium not included"; the ones on the main traffic are left out. */
function groupsText(tr: Tariff, names: Map<number, string>): string {
  return tr.pools
    .filter((p) => p.mode !== "main" && names.has(p.pool_id))
    .map((p) => `${names.get(p.pool_id)} ${modeShort(p.mode, p.traffic_limit)}`)
    .join(" · ");
}

/** The parts of a tariff "apply to the subscriptions given out" can bring its users up to. */
const APPLY_PARTS = ["traffic", "devices", "speed", "groups"] as const;

type ApplyPart = (typeof APPLY_PARTS)[number];

const APPLY_DEFAULTS: Record<ApplyPart, boolean> = { traffic: false, devices: false, speed: false, groups: true };

export function TariffsPage() {
  const { tab } = useSearch({ from: "/_app/tariffs" });
  const navigate = useNavigate({ from: "/tariffs" });
  const pools = usePools();
  const poolNames = new Map((pools.data ?? []).map((p) => [p.id, p.name]));
  const packages = usePackages();
  const tariffs = useTariffs();
  const selling = usePaymentSettings().data?.enabled === true;
  const [edit, setEdit] = useState<Tariff | "new" | null>(null);
  const [archive, setArchive] = useState<Tariff | null>(null);
  const [applying, setApplying] = useState<Tariff | null>(null);
  // The parts start as the defaults for every tariff: what was ticked for one is not applied
  // to the next by surprise.
  const [parts, setParts] = useState<Record<ApplyPart, boolean>>(APPLY_DEFAULTS);
  const openApply = (tr: Tariff) => {
    setParts(APPLY_DEFAULTS);
    setApplying(tr);
  };
  const [creating, setCreating] = useState<"pools" | "packages" | null>(null);
  const [rules, setRules] = useState(false);
  const qc = useQueryClient();
  const toast = useToast();
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/tariffs/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tariffs });
      void qc.invalidateQueries({ queryKey: qk.paymentSettings });
      toast.ok(t("tariffs.archived"));
      setArchive(null);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  // A tariff changed after it was given out reaches its subscriptions in the parts chosen.
  const apply = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/tariffs/{id}/apply", { params: { path: { id } }, body: parts })),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: qk.users });
      toast.ok(t("tariffs.apply.done", { n: r.applied }));
      setApplying(null);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const giftNames = (tr: Tariff) => tr.gifts.map((id) => packages.data?.find((p) => p.id === id)?.name).filter(Boolean).join(", ");
  const nodes = useNodes();
  const inbounds = useInbounds();
  // The chosen servers by name: the nodes whole, then the single protocols. Nothing while the
  // lists load ("none chosen" would be untrue); one that is gone is named by its id.
  const serversText = (tr: Tariff) => {
    if (!nodes.data || !inbounds.data) return null;
    const names = [
      ...tr.servers.nodes.map((id) => {
        const n = nodes.data.find((x) => x.id === id);
        return n ? nodeLabel(n) : `#${id}`;
      }),
      ...tr.servers.inbounds.map((id) => {
        const i = inbounds.data.find((x) => x.id === id);
        return i ? i.sub_name || i.title : `#${id}`;
      }),
    ];
    return names.length ? names.join(", ") : t("servers.none");
  };

  return (
    <>
      <PageHeader
        title={t("nav.tariffs")}
        sub={t("tariffs.subtitle")}
        actions={
          <>
            <Button onClick={() => setRules(true)} aria-label={t("lifecycle.open")}>
              <CalendarClock size={18} aria-hidden />
              <span className="max-[760px]:hidden">{t("lifecycle.open")}</span>
            </Button>
            {/* Each section adds its own kind of thing from the same place. */}
            <Button variant="primary" onClick={() => (tab === "tariffs" ? setEdit("new") : setCreating(tab))}>
              <Plus size={18} aria-hidden />
              <span className="max-[760px]:hidden">{tab === "pools" ? t("pools.add") : tab === "packages" ? t("packages.add") : t("tariffs.new")}</span>
            </Button>
          </>
        }
      />
      <Tabs
        id="tariffs"
        label={t("tariffs.sections")}
        tabs={TARIFF_TABS.map((id) => ({ id, label: t(`tariffs.tabs.${id}`), icon: TAB_ICONS[id] }))}
        value={tab}
        onChange={(next) => void navigate({ search: { tab: next }, replace: true })}
      >
        {tab === "pools" ? (
          <PoolsCard creating={creating === "pools"} onCreateClose={() => setCreating(null)} />
        ) : tab === "packages" ? (
          <PackagesCard creating={creating === "packages"} onCreateClose={() => setCreating(null)} />
        ) : (
          <QueryBoundary
            query={tariffs}
            pending={
              <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
                {[0, 1, 2].map((i) => (
                  <Skeleton key={i} style={{ height: 180, borderRadius: 20 }} />
                ))}
              </div>
            }
            wrap={(state) => <section className="card glass">{state}</section>}
          >
            {(list) =>
              list.length === 0 ? (
                <section className="card glass">
                  <EmptyState title={t("tariffs.emptyTitle")} text={t("tariffs.emptyText")}>
                    <Button variant="primary" onClick={() => setEdit("new")}>
                      <Plus size={18} aria-hidden /> {t("tariffs.new")}
                    </Button>
                  </EmptyState>
                </section>
              ) : (
                <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
                  {list.map((tr, i) => (
                    <section key={tr.id} className="card glass reveal flex flex-col" style={{ "--i": i } as React.CSSProperties}>
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0">
                          <h2 className="font-display text-xl font-medium tracking-tight">{tr.name}</h2>
                          {tr.price_label ? <div className="mt-1 text-[13px] font-medium text-[var(--mikan-700)]">{tr.price_label}</div> : null}
                          {tr.description ? <p className="mt-2 whitespace-pre-line text-[13px] text-[var(--ink-700)]">{tr.description}</p> : null}
                          {selling && tr.on_sale ? (
                            <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-[var(--ink-600)]">
                              <Pill tone="ok">{t("tariffs.onSale")}</Pill>
                              {tr.price_stars != null ? <span className="num">⭐ {tr.price_stars}</span> : null}
                              {tr.price_rub != null ? <span className="num">{rubles(tr.price_rub)}</span> : null}
                              {tr.terms.length > 1 ? <span>{t("tariffs.termsCount", { n: tr.terms.length })}</span> : null}
                            </div>
                          ) : null}
                        </div>
                        <div className="flex gap-1">
                          <button type="button" className="icon-btn" aria-label={t("tariffs.editLabel", { name: tr.name })} onClick={() => setEdit(tr)}>
                            <Pencil size={16} />
                          </button>
                          <button
                            type="button"
                            className="icon-btn"
                            aria-label={t("tariffs.apply.labelOf", { name: tr.name })}
                            title={tr.subscribers ? t("tariffs.apply.label") : t("tariffs.apply.none")}
                            disabled={!tr.subscribers}
                            onClick={() => openApply(tr)}
                          >
                            <RefreshCcw size={16} aria-hidden />
                          </button>
                          <button type="button" className="icon-btn" aria-label={t("tariffs.archiveLabel", { name: tr.name })} onClick={() => setArchive(tr)}>
                            <Archive size={16} />
                          </button>
                        </div>
                      </div>
                      <dl className="mt-5 grid grid-cols-2 gap-3 text-xs text-[var(--ink-500)]">
                        <Item label={t("users.colTraffic")} value={tr.traffic_limit != null ? bytes(tr.traffic_limit) : t("users.unlimited")} />
                        <Item label={t("users.colExpiry")} value={tr.starts_on_connect && tr.duration_days ? t("tariffs.termFromConnect", { term: tariffTerm(tr) }) : tariffTerm(tr)} />
                        <Item label={t("users.colDevices")} value={tr.device_limit != null ? (modeOf(tr) === "online" ? t("deviceMode.shortOnline", { n: tr.device_limit }) : String(tr.device_limit)) : t("users.unlimited")} />
                        <Item label={t("tariffs.speed")} value={tr.speed_limit != null ? t("tariffs.speedShort", { n: tr.speed_limit }) : t("users.unlimited")} />
                        <Item label={t("tariffs.reset")} value={resetLabel(tr)} />
                        <Item label={t("nav.users")} value={t("tariffs.subscribers", { n: tr.subscribers })} />
                        {tr.traffic_limit != null && tr.carry_over !== "inherit" ? <Item label={t("tariffs.carry")} value={t(`tariffs.carryShort.${tr.carry_over}`)} /> : null}
                        {tr.servers.all ? null : (
                          <div className="col-span-2">
                            <Item label={t("servers.label")} value={serversText(tr) ?? <Skeleton style={{ width: 120, height: 14 }} />} />
                          </div>
                        )}
                        {groupsText(tr, poolNames) ? (
                          <div className="col-span-2">
                            <Item label={t("pools.tariffLimits")} value={groupsText(tr, poolNames)} />
                          </div>
                        ) : null}
                        {giftNames(tr) ? (
                          <div className="col-span-2">
                            <Item label={t("tariffs.gifts")} value={giftNames(tr)} />
                          </div>
                        ) : null}
                      </dl>
                    </section>
                  ))}
                </div>
              )
            }
          </QueryBoundary>
        )}
      </Tabs>
      <TariffDrawer tariff={edit} onClose={() => setEdit(null)} />
      <LifecycleDrawer open={rules} onOpenChange={setRules} />
      <Confirm
        open={!!archive}
        onOpenChange={(v) => !v && setArchive(null)}
        title={t("tariffs.archiveTitle", { name: archive?.name ?? "" })}
        text={t("tariffs.archiveText")}
        confirm={t("tariffs.archiveConfirm")}
        loading={remove.isPending}
        onConfirm={() => archive && remove.mutate(archive.id)}
      />
      <Confirm
        open={!!applying}
        onOpenChange={(v) => !v && setApplying(null)}
        title={t("tariffs.apply.title", { name: applying?.name ?? "" })}
        text={applying ? t("tariffs.apply.text", { n: applying.subscribers }) : ""}
        confirm={t("tariffs.apply.confirm")}
        confirmDisabled={!APPLY_PARTS.some((p) => parts[p])}
        loading={apply.isPending}
        onConfirm={() => applying && apply.mutate(applying.id)}
      >
        <fieldset className="mt-4 flex flex-col gap-1" aria-label={t("tariffs.apply.label")}>
          {APPLY_PARTS.map((p) => (
            <label key={p} className="flex items-center gap-2 py-1.5 text-[13px]">
              <input type="checkbox" className="check" checked={parts[p]} onChange={(e) => setParts({ ...parts, [p]: e.target.checked })} />
              {t(`tariffs.apply.${p}`)}
            </label>
          ))}
        </fieldset>
      </Confirm>
    </>
  );
}

function Item({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd className="mt-0.5 text-sm font-medium text-[var(--ink-900)]">{value}</dd>
    </div>
  );
}
