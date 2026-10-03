import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Archive, Layers, Package, Pencil, Plus, Tag, X } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError, errorText, unwrap, type Schemas, type Tariff } from "../../api/client";
import { qk, usePaymentSettings, usePools, useTariffs } from "../../api/hooks";
import { Confirm, Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { Tabs } from "../../components/tabs";
import { useToast } from "../../components/toast";
import { Button, EmptyState, Field, PageHeader, Pill, Segmented, Skeleton } from "../../components/ui";
import { Switch } from "../../components/switch";
import { t } from "../../i18n";
import { bytes, days, GiB, months, rubles, termMonths } from "../../lib/format";
import { TARIFF_TABS } from "../search";
import { PackagesCard } from "./packages";
import { PoolLimitsField, PoolsCard } from "./pools";

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
    tr.device_limit != null ? t("userDrawer.devicesShort", { n: tr.device_limit }) : t("tariffs.devicesUnlimitedShort"),
  ];
  return parts.join(" · ");
}

const TAB_ICONS = { tariffs: Tag, pools: Layers, packages: Package } as const;

/** A tariff's pool limits by pool name: "WL 100 GB"; pools without a limit are left out. */
function poolLimitsText(tr: Tariff, names: Map<number, string>): string {
  return tr.pools
    .filter((p) => p.traffic_limit != null && names.has(p.pool_id))
    .map((p) => `${names.get(p.pool_id)} ${bytes(p.traffic_limit!)}`)
    .join(" · ");
}

export function TariffsPage() {
  const { tab } = useSearch({ from: "/_app/tariffs" });
  const navigate = useNavigate({ from: "/tariffs" });
  const pools = usePools();
  const poolNames = new Map((pools.data ?? []).map((p) => [p.id, p.name]));
  const tariffs = useTariffs();
  const selling = usePaymentSettings().data?.enabled === true;
  const [edit, setEdit] = useState<Tariff | "new" | null>(null);
  const [archive, setArchive] = useState<Tariff | null>(null);
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

  return (
    <>
      <PageHeader
        title={t("nav.tariffs")}
        sub={t("tariffs.subtitle")}
        actions={
          tab === "tariffs" ? (
            <Button variant="primary" onClick={() => setEdit("new")}>
              <Plus size={18} aria-hidden />
              <span className="max-[760px]:hidden">{t("tariffs.new")}</span>
            </Button>
          ) : null
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
          <div className="max-w-4xl">
            <PoolsCard />
          </div>
        ) : tab === "packages" ? (
          <div className="max-w-4xl">
            <PackagesCard />
          </div>
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
                        <div>
                          <h2 className="font-display text-xl font-medium tracking-tight">{tr.name}</h2>
                          {tr.price_label ? <div className="mt-1 text-[13px] font-medium text-[var(--mikan-700)]">{tr.price_label}</div> : null}
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
                          <button type="button" className="icon-btn" aria-label={t("tariffs.archiveLabel", { name: tr.name })} onClick={() => setArchive(tr)}>
                            <Archive size={16} />
                          </button>
                        </div>
                      </div>
                      <dl className="mt-5 grid grid-cols-2 gap-3 text-xs text-[var(--ink-500)]">
                        <Item label={t("users.colTraffic")} value={tr.traffic_limit != null ? bytes(tr.traffic_limit) : t("users.unlimited")} />
                        <Item label={t("users.colExpiry")} value={tariffTerm(tr)} />
                        <Item label={t("users.colDevices")} value={tr.device_limit != null ? String(tr.device_limit) : t("users.unlimited")} />
                        <Item label={t("tariffs.reset")} value={resetLabel(tr)} />
                        {poolLimitsText(tr, poolNames) ? (
                          <div className="col-span-2">
                            <Item label={t("pools.tariffLimits")} value={poolLimitsText(tr, poolNames)} />
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
      <Confirm
        open={!!archive}
        onOpenChange={(v) => !v && setArchive(null)}
        title={t("tariffs.archiveTitle", { name: archive?.name ?? "" })}
        text={t("tariffs.archiveText")}
        confirm={t("tariffs.archiveConfirm")}
        loading={remove.isPending}
        onConfirm={() => archive && remove.mutate(archive.id)}
      />
    </>
  );
}

function Item({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd className="mt-0.5 text-sm font-medium text-[var(--ink-900)]">{value}</dd>
    </div>
  );
}

function TariffDrawer({ tariff, onClose }: { tariff: Tariff | "new" | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState("");
  const [gb, setGb] = useState("150");
  const [unlimited, setUnlimited] = useState(false);
  const [duration, setDuration] = useState("30");
  const [term, setTerm] = useState<"days" | "day">("days");
  const [monthsN, setMonthsN] = useState("1");
  const [billingDay, setBillingDay] = useState("1");
  const [devices, setDevices] = useState("3");
  const [devicesUnlimited, setDevicesUnlimited] = useState(false);
  const [reset, setReset] = useState<Tariff["reset_strategy"]>("period");
  const [price, setPrice] = useState("");
  const [onSale, setOnSale] = useState(false);
  const allPools = usePools();
  // With selling off the sale block is hidden; its values stay as they were.
  const selling = usePaymentSettings().data?.enabled === true;
  const [poolGB, setPoolGB] = useState<Record<number, string>>({});
  const [stars, setStars] = useState("");
  const [rub, setRub] = useState("");
  // The terms after the first: days, or months with a billing day, and their prices.
  const [more, setMore] = useState<ExtraTerm[]>([]);
  const [errors, setErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    if (!tariff) return;
    const tr = tariff === "new" ? null : tariff;
    setName(tr?.name ?? "");
    setUnlimited(tr ? tr.traffic_limit == null : false);
    setGb(tr?.traffic_limit != null ? String(Math.round(tr.traffic_limit / GiB)) : "150");
    setDuration(String(tr?.duration_days ?? 30));
    setTerm(tr?.billing_day != null ? "day" : "days");
    setMonthsN(tr?.billing_day != null && !tr.duration_days ? "0" : String(termMonths(tr?.duration_days ?? 30)));
    setBillingDay(String(tr?.billing_day ?? new Date().getDate()));
    setDevicesUnlimited(tr ? tr.device_limit == null : false);
    setDevices(String(tr?.device_limit ?? 3));
    setReset(tr?.reset_strategy ?? "period");
    setPrice(tr?.price_label ?? "");
    setOnSale(tr?.on_sale ?? false);
    setPoolGB(Object.fromEntries((tr?.pools ?? []).map((p) => [p.pool_id, p.traffic_limit != null ? String(+(p.traffic_limit / GiB).toFixed(2)) : ""])));
    setStars(tr?.price_stars != null ? String(tr.price_stars) : "");
    setRub(tr?.price_rub != null ? String(tr.price_rub / 100) : "");
    setMore(
      (tr?.terms ?? []).slice(1).map((x) => ({
        key: nextKey++,
        days: tr?.billing_day != null ? (x.days ? String(termMonths(x.days)) : "0") : String(x.days),
        stars: x.price_stars != null ? String(x.price_stars) : "",
        rub: x.price_rub != null ? String(x.price_rub / 100) : "",
      })),
    );
    setErrors({});
  }, [tariff]);

  const save = useMutation({
    mutationFn: (body: Schemas["TariffBody"]) =>
      tariff === "new" || !tariff ? unwrap(api.POST("/api/v1/tariffs", { body })) : unwrap(api.PUT("/api/v1/tariffs/{id}", { params: { path: { id: tariff.id } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tariffs });
      void qc.invalidateQueries({ queryKey: qk.paymentSettings });
      toast.ok(tariff === "new" ? t("tariffs.created") : t("tariffs.saved"));
      onClose();
    },
    onError: (e) => {
      if (e instanceof ApiError && Object.keys(e.fields).length) setErrors(e.fields);
      else toast.error(errorText(e));
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    const gbN = Number(gb);
    const durN = Number(duration);
    const monN = Number(monthsN);
    const dayN = Number(billingDay);
    const devN = Number(devices);
    const poolLimits = (allPools.data ?? []).map((p) => {
      const v = (poolGB[p.id] ?? "").trim().replace(",", ".");
      return { pool_id: p.id, traffic_limit: v ? Math.round(Number(v) * GiB) : null };
    });
    if (poolLimits.some((p) => p.traffic_limit !== null && (!Number.isFinite(p.traffic_limit) || p.traffic_limit <= 0))) errs.pools = t("pools.errLimit");
    const toDay = term === "day";
    if (!name.trim()) errs.name = t("tariffs.errName");
    if (!unlimited && (!Number.isFinite(gbN) || gbN <= 0)) errs.traffic_limit = t("tariffs.errTraffic");
    if (!toDay && (!Number.isInteger(durN) || durN < 0 || durN > 3650)) errs.duration_days = t("tariffs.errDuration");
    if (toDay && (!Number.isInteger(monN) || monN < 0 || monN > 120)) errs.duration_days = t("tariffs.errMonths");
    if (toDay && (!Number.isInteger(dayN) || dayN < 1 || dayN > 31)) errs.billing_day = t("tariffs.errBillingDay");
    if (!devicesUnlimited && (!Number.isInteger(devN) || devN < 1 || devN > 100)) errs.device_limit = t("tariffs.errDevices");
    const starsN = Number(stars);
    const rubN = Math.round(Number(rub.replace(",", ".")) * 100);
    if (stars.trim() && (!Number.isInteger(starsN) || starsN < 1 || starsN > 10000)) errs.price_stars = t("tariffs.errStars");
    if (rub.trim() && (!Number.isFinite(rubN) || rubN < 100 || rubN > 100000000)) errs.price_rub = t("tariffs.errRub");
    if (onSale && !stars.trim() && !rub.trim()) errs.on_sale = t("errors.api.on_sale_no_price");
    const firstDays = toDay ? monN * 30 : durN;
    const seen = new Set([firstDays]);
    const terms: Schemas["TermBody"][] = [{ days: firstDays, price_stars: stars.trim() ? starsN : undefined, price_rub: rub.trim() ? rubN : undefined }];
    if (more.length && !stars.trim() && !rub.trim()) errs.price_rub = t("errors.api.term_no_price");
    more.forEach((m, i) => {
      const at = `terms[${i + 1}]`;
      const n = Number(m.days);
      const d = toDay ? n * 30 : n;
      if (!Number.isInteger(n) || n < 0 || (toDay ? n > 120 : n > 3650)) errs[`${at}.days`] = t(toDay ? "tariffs.errMonths" : "tariffs.errDuration");
      else if (seen.has(d)) errs[`${at}.days`] = t("errors.api.term_days_repeat");
      seen.add(d);
      const s = Number(m.stars);
      const r = Math.round(Number(m.rub.replace(",", ".")) * 100);
      if (m.stars.trim() && (!Number.isInteger(s) || s < 1 || s > 10000)) errs[`${at}.price_stars`] = t("tariffs.errStars");
      if (m.rub.trim() && (!Number.isFinite(r) || r < 100 || r > 100000000)) errs[`${at}.price_rub`] = t("tariffs.errRub");
      if (!m.stars.trim() && !m.rub.trim()) errs[`${at}.price_rub`] = t("errors.api.term_no_price");
      terms.push({ days: d, price_stars: m.stars.trim() ? s : undefined, price_rub: m.rub.trim() ? r : undefined });
    });
    setErrors(errs);
    if (Object.keys(errs).length) return;
    save.mutate({
      name: name.trim(),
      traffic_limit: unlimited ? undefined : Math.round(gbN * GiB),
      // The server turns 30 days into one month up to the billing day.
      duration_days: toDay ? monN * 30 : durN,
      billing_day: toDay ? dayN : undefined,
      device_limit: devicesUnlimited ? undefined : devN,
      reset_strategy: unlimited ? "none" : reset,
      price_label: price.trim() || undefined,
      price_stars: stars.trim() ? starsN : undefined,
      price_rub: rub.trim() ? rubN : undefined,
      terms,
      on_sale: onSale,
      pools: poolLimits,
      // PUT replaces the tariff: keep its place in the list.
      sort: tariff && tariff !== "new" ? tariff.sort : undefined,
    });
  };

  return (
    <Drawer
      open={!!tariff}
      onOpenChange={(v) => !v && onClose()}
      title={tariff === "new" ? t("tariffs.new") : t("users.colTariff")}
      meta={t("tariffs.drawerMeta")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" type="submit" form="tariff-form" loading={save.isPending}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form id="tariff-form" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("tariffs.name")} htmlFor="t-name" error={errors.name}>
          <input id="t-name" className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={60} placeholder={t("tariffs.namePlaceholder")} autoFocus />
        </Field>
        <Field label={t("tariffs.trafficPerPeriod")} htmlFor="t-gb" error={errors.traffic_limit}>
          <div className="flex items-center gap-2">
            <input id="t-gb" className="input max-w-[140px]" inputMode="decimal" value={unlimited ? "" : gb} disabled={unlimited} onChange={(e) => setGb(e.target.value)} aria-invalid={!!errors.traffic_limit} />
            <span className="text-[var(--ink-500)]">{t("units.gb")}</span>
            <label className="ml-auto flex items-center gap-2 text-[13px]">
              <input type="checkbox" className="check" checked={unlimited} onChange={(e) => setUnlimited(e.target.checked)} /> {t("users.unlimited")}
            </label>
          </div>
        </Field>
        <Field label={t("users.colExpiry")} htmlFor={term === "day" ? "t-months" : "t-days"} hint={term === "day" ? t("tariffs.monthsHint") : t("tariffs.durationHint")} error={errors.duration_days}>
          <Segmented
            label={t("tariffs.termKind")}
            value={term}
            onChange={setTerm}
            options={[
              { value: "days", label: t("tariffs.termDays") },
              { value: "day", label: t("tariffs.termToDate") },
            ]}
          />
          {term === "day" ? (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <input id="t-months" className="input max-w-[80px]" inputMode="numeric" value={monthsN} onChange={(e) => setMonthsN(e.target.value)} aria-invalid={!!errors.duration_days} />
              {[1, 3, 6, 12].map((n) => (
                <button key={n} type="button" className="chip-btn" onClick={() => setMonthsN(String(n))}>
                  {n === 12 ? t("userDrawer.year") : months(n)}
                </button>
              ))}
            </div>
          ) : (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <input id="t-days" className="input max-w-[100px]" inputMode="numeric" value={duration} onChange={(e) => setDuration(e.target.value)} aria-invalid={!!errors.duration_days} />
              {[3, 7, 30, 90, 365].map((n) => (
                <button key={n} type="button" className="chip-btn" onClick={() => setDuration(String(n))}>
                  {n === 365 ? t("userDrawer.year") : days(n)}
                </button>
              ))}
            </div>
          )}
        </Field>
        {term === "day" ? (
          <Field label={t("tariffs.billingDay")} htmlFor="t-bday" hint={t("tariffs.billingDayHint")} error={errors.billing_day}>
            <div className="flex items-center gap-2">
              <input id="t-bday" className="input max-w-[100px]" inputMode="numeric" value={billingDay} onChange={(e) => setBillingDay(e.target.value)} aria-invalid={!!errors.billing_day} />
              <span className="text-[var(--ink-500)]">{t("tariffs.dayOfEveryMonth")}</span>
            </div>
          </Field>
        ) : null}
        <Field label={t("tariffs.devicesAtOnce")} htmlFor="t-dev" error={errors.device_limit}>
          <div className="flex items-center gap-2">
            <input id="t-dev" className="input max-w-[100px]" inputMode="numeric" value={devicesUnlimited ? "" : devices} disabled={devicesUnlimited} onChange={(e) => setDevices(e.target.value)} />
            <label className="ml-auto flex items-center gap-2 text-[13px]">
              <input type="checkbox" className="check" checked={devicesUnlimited} onChange={(e) => setDevicesUnlimited(e.target.checked)} /> {t("users.unlimited")}
            </label>
          </div>
        </Field>
        {!unlimited ? (
          <Field label={t("tariffs.whenReset")} hint={t("tariffs.whenResetHint")}>
            <Segmented
              label={t("tariffs.reset")}
              value={reset}
              onChange={setReset}
              options={[
                { value: "period", label: t("tariffs.resetEvery30") },
                { value: "month_start", label: term === "day" && Number(billingDay) >= 1 && Number(billingDay) <= 31 ? t("tariffs.resetOnDayShort", { d: Number(billingDay) }) : t("tariffs.resetMonthStart") },
                { value: "none", label: t("tariffs.resetNever") },
              ]}
            />
          </Field>
        ) : null}
        {allPools.data?.length ? (
          <Field label={t("pools.tariffLimits")} hint={t("pools.tariffLimitsHint")} error={errors.pools}>
            <PoolLimitsField pools={allPools.data} value={poolGB} onChange={setPoolGB} />
          </Field>
        ) : null}
        <div className="border-t border-[var(--hairline)] pt-4" role="group" aria-label={t("tariffs.sale")}>
          {selling ? (
            <>
              <div className="mb-3 flex items-start justify-between gap-3">
                <div>
                  <div className="text-[13px] font-semibold">{t("tariffs.sale")}</div>
                  <div className="text-xs text-[var(--ink-500)]">{t("tariffs.saleSub")}</div>
                </div>
                <Switch checked={onSale} onChange={setOnSale} label={t("tariffs.onSale")} />
              </div>
              {errors.on_sale ? (
                <p className="mb-3 text-xs text-[var(--berry-600)]" role="alert">
                  {errors.on_sale}
                </p>
              ) : null}
              <div className="grid gap-x-3 sm:grid-cols-2">
                <Field label={t("tariffs.priceStars")} htmlFor="t-stars" hint={t("tariffs.priceStarsHint")} error={errors.price_stars}>
                  <div className="flex items-center gap-2">
                    <input id="t-stars" className="input max-w-[140px]" inputMode="numeric" value={stars} onChange={(e) => setStars(e.target.value)} placeholder="150" aria-invalid={!!errors.price_stars} />
                    <span className="text-[var(--ink-500)]">⭐</span>
                  </div>
                </Field>
                <Field label={t("tariffs.priceRub")} htmlFor="t-rub" hint={t("tariffs.priceRubHint")} error={errors.price_rub}>
                  <div className="flex items-center gap-2">
                    <input id="t-rub" className="input max-w-[140px]" inputMode="decimal" value={rub} onChange={(e) => setRub(e.target.value)} placeholder="199" aria-invalid={!!errors.price_rub} />
                    <span className="text-[var(--ink-500)]">₽</span>
                  </div>
                </Field>
              </div>
              <MoreTerms value={more} onChange={setMore} toDay={term === "day"} errors={errors} />
            </>
          ) : null}
          <Field label={t("tariffs.price")} htmlFor="t-price" hint={t("tariffs.priceHint")}>
            <input id="t-price" className="input" value={price} onChange={(e) => setPrice(e.target.value)} maxLength={40} placeholder={t("tariffs.pricePlaceholder")} />
          </Field>
        </div>
      </form>
    </Drawer>
  );
}

type ExtraTerm = { key: number; days: string; stars: string; rub: string };

let nextKey = 0;

/** The terms a tariff is sold for after its first: each its own days (or months up to the
 * billing day) and prices. The bot and the Mini App let the buyer pick one. */
function MoreTerms({ value, onChange, toDay, errors }: { value: ExtraTerm[]; onChange: (v: ExtraTerm[]) => void; toDay: boolean; errors: Record<string, string> }) {
  const set = (i: number, k: "days" | "stars" | "rub", v: string) => onChange(value.map((m, j) => (j === i ? { ...m, [k]: v } : m)));
  return (
    <div className="mb-4" role="group" aria-label={t("tariffs.moreTerms")}>
      <div className="text-[13px] font-semibold">{t("tariffs.moreTerms")}</div>
      <div className="mb-2 text-xs text-[var(--ink-500)]">{t("tariffs.moreTermsSub")}</div>
      {value.map((m, i) => {
        const at = `terms[${i + 1}]`;
        const err = errors[`${at}.days`] ?? errors[`${at}.price_stars`] ?? errors[`${at}.price_rub`];
        return (
          <div key={m.key} className="mb-2">
            <div className="grid grid-cols-[1fr_1fr_1fr_auto] items-center gap-2">
              <label className="input-unit min-w-0">
                <input
                  className="input num"
                  style={{ paddingRight: 48 }}
                  inputMode="numeric"
                  value={m.days}
                  onChange={(e) => set(i, "days", e.target.value)}
                  aria-label={t(toDay ? "tariffs.termMonthsLabel" : "tariffs.termDaysLabel", { n: i + 2 })}
                  aria-invalid={!!errors[`${at}.days`]}
                />
                <span>{t(toDay ? "tariffs.monthsShort" : "tariffs.daysShort")}</span>
              </label>
              <label className="input-unit min-w-0">
                <input className="input num" inputMode="numeric" value={m.stars} onChange={(e) => set(i, "stars", e.target.value)} aria-label={t("tariffs.priceStars")} aria-invalid={!!errors[`${at}.price_stars`]} />
                <span>⭐</span>
              </label>
              <label className="input-unit min-w-0">
                <input className="input num" inputMode="decimal" value={m.rub} onChange={(e) => set(i, "rub", e.target.value)} aria-label={t("tariffs.priceRub")} aria-invalid={!!errors[`${at}.price_rub`]} />
                <span>₽</span>
              </label>
              <button type="button" className="icon-btn" aria-label={t("tariffs.removeTerm")} onClick={() => onChange(value.filter((_, j) => j !== i))}>
                <X size={16} />
              </button>
            </div>
            {err ? (
              <p className="mt-1 text-xs text-[var(--berry-600)]" role="alert">
                {err}
              </p>
            ) : null}
          </div>
        );
      })}
      {value.length < 11 ? (
        <Button type="button" size="sm" variant="ghost" onClick={() => onChange([...value, { key: nextKey++, days: "", stars: "", rub: "" }])}>
          <Plus size={16} aria-hidden /> {t("tariffs.addTerm")}
        </Button>
      ) : null}
    </div>
  );
}
