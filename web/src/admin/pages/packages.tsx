// Traffic packages (GitHub issue #12): extra traffic for the main quota or one pool,
// sold in the bot and the Mini App like tariffs. The base quota of the period is spent
// first, then the packages, the soonest to expire first.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Archive, Pencil } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { onFormError } from "../../lib/fields";
import { qk, usePackages, usePaymentSettings, usePools } from "../../api/hooks";
import { Confirm, Drawer, DrawerFooter } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Field, Pill, Segmented, Skeleton } from "../../components/ui";
import { Switch } from "../../components/switch";
import { t } from "../../i18n";
import { bytes, days as daysText, GiB, rubles } from "../../lib/format";
import { isKopecks, isLifetimeDays, isStars, isTrafficGB, LIFETIME_DAYS_PRESETS, parseDecimal, rubKopecks } from "../../lib/validate";

type Package = Schemas["PackageView"];
type Pool = Schemas["PoolView"];
export type Lifetime = Package["lifetime"];

/** Where a package or a grant adds traffic: the main quota or a pool by name. */
export function targetName(poolId: number | null | undefined, pools: Pool[] | undefined): string {
  if (poolId == null) return t("pools.mainTraffic");
  return pools?.find((p) => p.id === poolId)?.name ?? t("packages.poolGone");
}

/** How long a package lasts: until used up, until the period ends, or N days. */
function lifetimeText(lifetime: Lifetime, days: number): string {
  if (lifetime === "days") return t("packages.lifetimeDaysN", { days: daysText(days) });
  return t(`packages.lifetime.${lifetime}`);
}

/** The packages; a new one is started from the page's header (`creating`) or the empty list. */
export function PackagesCard({ creating, onCreateClose }: { creating: boolean; onCreateClose: () => void }) {
  const packages = usePackages();
  const pools = usePools();
  const selling = usePaymentSettings().data?.enabled === true;
  const qc = useQueryClient();
  const toast = useToast();
  const [edit, setEdit] = useState<Package | "new" | null>(null);
  const [archive, setArchive] = useState<Package | null>(null);
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/packages/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.packages });
      setArchive(null);
      toast.ok(t("packages.archived"));
    },
    onError: (e) => toast.error(errorText(e)),
  });

  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("packages.title")}</h2>
          <div className="card-sub">{t("packages.sub")}</div>
        </div>
      </div>
      <QueryBoundary query={packages} pending={<Skeleton style={{ height: 64 }} />}>
        {(list) =>
          list.length === 0 ? (
            <p className="text-xs text-[var(--ink-500)]">{t("packages.none")}</p>
          ) : (
            <ul className="row-list">
              {list.map((p) => (
                <li key={p.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
                  <div className="min-w-0">
                    <div className="text-[13px] font-semibold">{p.name}</div>
                    <div className="text-xs text-[var(--ink-500)]">
                      {[bytes(p.bytes), targetName(p.pool_id, pools.data), lifetimeText(p.lifetime, p.days)].join(" · ")}
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--ink-600)]">
                      {p.on_sale && selling ? <Pill tone="ok">{t("tariffs.onSale")}</Pill> : <Pill tone="off">{t("packages.notOnSale")}</Pill>}
                      {p.price_stars != null ? <span className="num">⭐ {p.price_stars}</span> : null}
                      {p.price_rub != null ? <span className="num">{rubles(p.price_rub)}</span> : null}
                    </div>
                  </div>
                  <div className="flex gap-1">
                    <button type="button" className="icon-btn" aria-label={t("packages.editLabel", { name: p.name })} onClick={() => setEdit(p)}>
                      <Pencil size={16} />
                    </button>
                    <button type="button" className="icon-btn" aria-label={t("packages.archiveLabel", { name: p.name })} onClick={() => setArchive(p)}>
                      <Archive size={16} />
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          )
        }
      </QueryBoundary>
      <PackageDrawer
        pkg={creating ? "new" : edit}
        pools={pools.data ?? []}
        selling={selling}
        onClose={() => {
          setEdit(null);
          onCreateClose();
        }}
      />
      <Confirm
        open={!!archive}
        onOpenChange={(v) => !v && setArchive(null)}
        title={t("packages.archiveTitle", { name: archive?.name ?? "" })}
        text={t("packages.archiveText")}
        confirm={t("tariffs.archiveConfirm")}
        loading={remove.isPending}
        onConfirm={() => archive && remove.mutate(archive.id)}
      />
    </section>
  );
}

function PackageDrawer({ pkg, pools, selling, onClose }: { pkg: Package | "new" | null; pools: Pool[]; selling: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState("");
  const [gb, setGb] = useState("50");
  const [pool, setPool] = useState(0);
  const [lifetime, setLifetime] = useState<Lifetime>("used");
  const [days, setDays] = useState("30");
  const [stars, setStars] = useState("");
  const [rub, setRub] = useState("");
  const [onSale, setOnSale] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    if (!pkg) return;
    const p = pkg === "new" ? null : pkg;
    setName(p?.name ?? "");
    setGb(p ? String(+(p.bytes / GiB).toFixed(2)) : "50");
    setPool(p?.pool_id ?? 0);
    setLifetime(p?.lifetime ?? "used");
    setDays(String(p?.days || 30));
    setStars(p?.price_stars != null ? String(p.price_stars) : "");
    setRub(p?.price_rub != null ? String(p.price_rub / 100) : "");
    setOnSale(p?.on_sale ?? false);
    setErrors({});
  }, [pkg]);

  const save = useMutation({
    mutationFn: (body: Schemas["PackageBody"]) =>
      pkg === "new" || !pkg ? unwrap(api.POST("/api/v1/packages", { body })) : unwrap(api.PUT("/api/v1/packages/{id}", { params: { path: { id: pkg.id } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.packages });
      toast.ok(pkg === "new" ? t("packages.created") : t("packages.saved"));
      onClose();
    },
    onError: (e) => {
      onFormError(e, toast, setErrors);
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const errs: Record<string, string> = {};
    const gbN = parseDecimal(gb);
    const daysN = Number(days);
    const starsN = Number(stars);
    const rubN = rubKopecks(rub);
    if (!name.trim()) errs.name = t("packages.errName");
    if (!isTrafficGB(gbN)) errs.bytes = t("errors.api.bad_bytes");
    if (lifetime === "days" && !isLifetimeDays(daysN)) errs.days = t("errors.api.bad_days");
    if (stars.trim() && !isStars(starsN)) errs.price_stars = t("tariffs.errStars");
    if (rub.trim() && !isKopecks(rubN)) errs.price_rub = t("tariffs.errRub");
    if (onSale && !stars.trim() && !rub.trim()) errs.on_sale = t("errors.api.on_sale_no_price");
    setErrors(errs);
    if (Object.keys(errs).length) return;
    save.mutate({
      name: name.trim(),
      bytes: Math.round(gbN * GiB),
      pool_id: pool || undefined,
      lifetime,
      days: lifetime === "days" ? daysN : undefined,
      price_stars: stars.trim() ? starsN : undefined,
      price_rub: rub.trim() ? rubN : undefined,
      on_sale: onSale,
      // PUT replaces the package: keep its place in the list.
      sort: pkg && pkg !== "new" ? pkg.sort : undefined,
    });
  };

  return (
    <Drawer
      open={!!pkg}
      onOpenChange={(v) => !v && onClose()}
      title={pkg === "new" ? t("packages.new") : t("packages.edit")}
      meta={t("packages.drawerMeta")}
      footer={
        <DrawerFooter form="package-form" onCancel={onClose} loading={save.isPending}>
          {t("common.save")}
        </DrawerFooter>
      }
    >
      <form id="package-form" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("packages.name")} htmlFor="pk-name" error={errors.name}>
          <input id="pk-name" className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={60} placeholder={t("packages.namePlaceholder")} aria-invalid={!!errors.name} autoFocus />
        </Field>
        <Field label={t("packages.size")} htmlFor="pk-gb" error={errors.bytes}>
          <div className="flex items-center gap-2">
            <input id="pk-gb" className="input max-w-[140px]" inputMode="decimal" value={gb} onChange={(e) => setGb(e.target.value)} aria-invalid={!!errors.bytes} />
            <span className="text-[var(--ink-500)]">{t("units.gb")}</span>
          </div>
        </Field>
        <Field label={t("packages.target")} htmlFor="pk-pool" hint={t("packages.targetHint")} error={errors.pool_id}>
          <select id="pk-pool" className="input max-w-[320px]" value={pool} onChange={(e) => setPool(Number(e.target.value))}>
            <option value={0}>{t("pools.mainTraffic")}</option>
            {pools.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t("packages.lifetimeLabel")} htmlFor={lifetime === "days" ? "pk-days" : undefined} hint={t(`packages.lifetimeHint.${lifetime}`)} error={errors.days ?? errors.lifetime}>
          <Segmented
            label={t("packages.lifetimeLabel")}
            value={lifetime}
            onChange={setLifetime}
            options={[
              { value: "used", label: t("packages.lifetime.used") },
              { value: "period", label: t("packages.lifetime.period") },
              { value: "days", label: t("packages.lifetimeDays") },
            ]}
          />
          {lifetime === "days" ? (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <input id="pk-days" className="input max-w-[100px]" inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} aria-invalid={!!errors.days} />
              {LIFETIME_DAYS_PRESETS.map((n) => (
                <button key={n} type="button" className="chip-btn" onClick={() => setDays(String(n))}>
                  {daysText(n)}
                </button>
              ))}
            </div>
          ) : null}
        </Field>
        <div className="border-t border-[var(--hairline)] pt-4" role="group" aria-label={t("tariffs.sale")}>
          <div className="mb-3 flex items-start justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold">{t("tariffs.sale")}</div>
              <div className="text-xs text-[var(--ink-500)]">{selling ? t("packages.saleSub") : t("packages.sellingOff")}</div>
            </div>
            <Switch checked={onSale} onChange={setOnSale} label={t("tariffs.onSale")} />
          </div>
          {errors.on_sale ? (
            <p className="mb-3 text-xs text-[var(--berry-600)]" role="alert">
              {errors.on_sale}
            </p>
          ) : null}
          <div className="grid gap-x-3 sm:grid-cols-2">
            <Field label={t("tariffs.priceStars")} htmlFor="pk-stars" error={errors.price_stars}>
              <div className="flex items-center gap-2">
                <input id="pk-stars" className="input max-w-[140px]" inputMode="numeric" value={stars} onChange={(e) => setStars(e.target.value)} placeholder="75" aria-invalid={!!errors.price_stars} />
                <span className="text-[var(--ink-500)]">⭐</span>
              </div>
            </Field>
            <Field label={t("tariffs.priceRub")} htmlFor="pk-rub" hint={t("tariffs.priceRubHint")} error={errors.price_rub}>
              <div className="flex items-center gap-2">
                <input id="pk-rub" className="input max-w-[140px]" inputMode="decimal" value={rub} onChange={(e) => setRub(e.target.value)} placeholder="79" aria-invalid={!!errors.price_rub} />
                <span className="text-[var(--ink-500)]">₽</span>
              </div>
            </Field>
          </div>
        </div>
      </form>
    </Drawer>
  );
}
