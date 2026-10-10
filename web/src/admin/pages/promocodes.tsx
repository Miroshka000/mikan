import { Pencil, Plus, Search, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState, type FormEvent } from "react";
import { errorText } from "../../api/client";
import { usePools, usePromoMutations, usePromoRedemptions, usePromocodes, useTariffs, type PromoCode, type PromoRedemption } from "../../api/hooks";
import { Disclosure } from "../../components/layout";
import { Confirm, Drawer } from "../../components/overlay";
import { Switch, SwitchRow } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Button, EmptyState, ErrorState, Field, Pill, Segmented, Skeleton } from "../../components/ui";
import { t, tMaybe } from "../../i18n";
import { dateShort, money, time } from "../../lib/format";
import { PROMO_TABS } from "../search";

type PromoTab = (typeof PROMO_TABS)[number];

const empty: PromoCode = {
  id: 0, code: "", name: "", description: "", type: "days", value: 30, currency: "", used_count: 0,
  per_user_limit: 1, discount_ttl: 0, min_order: 0, max_discount: 0, tariff_ids: [], first_purchase_only: false,
  new_users_only: false, enabled: true, status: "active", created_at: "",
};

type Draft = Omit<PromoCode, "created_at" | "created_by" | "min_order" | "max_discount"> & {
  starts_at: string; ends_at: string; max_uses: string | number | undefined; value: string | number; min_order: string | number; max_discount: string | number;
};

const GB = 1073741824;

// Sums are kept in the payment's smallest unit (kopecks, Stars); the form shows rubles.
function fromUnits(n: number, currency: string) {
  return currency === "RUB" ? n / 100 : n;
}

function toUnits(v: string | number, currency: string) {
  const n = Number(v) || 0;
  return Math.round(currency === "RUB" ? n * 100 : n);
}

function unitOf(currency: string) {
  return currency === "RUB" ? "₽" : "⭐";
}

function dateInput(value?: string) {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// Chrome takes up to six digits for the year and moves on to the time only after them, so
// "2026" and then "00" for the hours makes the year 202600. A max with a four-digit year
// makes it move on after four.
const DATE_MIN = "2000-01-01T00:00";
const DATE_MAX = "9999-12-31T23:59";

/** A filled date field that is not a date in four digits: saving it would drop it silently. */
function badDate(value: string) {
  if (!value) return false;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) || d.getFullYear() > 9999;
}

function epoch(value?: string) {
  if (!value) return undefined;
  const ms = new Date(value).getTime();
  return Number.isFinite(ms) ? Math.floor(ms / 1000) : undefined;
}

function statusTone(status: string): "ok" | "off" | "warn" {
  return status === "active" ? "ok" : status === "disabled" ? "off" : "warn";
}

function statusText(status: string) {
  return tMaybe(`promocodes.status.${status}`) ?? status;
}

function redemptionStatusText(status: string) {
  return tMaybe(`promocodes.historyStatus.${status}`) ?? status;
}

function valueText(p: PromoCode) {
  if (p.type === "traffic") return `${(p.value / GB).toFixed(1)} ${t("promocodes.trafficUnit")}`;
  if (p.type === "days") return `${p.value} ${t("promocodes.daysUnit")}`;
  return p.type === "percent" ? `${p.value}%` : money(p.value, p.currency);
}

/** Promo codes: the codes with their switches, and the history of what they gave. */
/**
 * Promo codes, a tab of Payments: the codes and their activations, and the editor. sub is
 * which of the two shows (the address keeps it), onSub switches it.
 */
export function PromoSection({ sub, onSub }: { sub: PromoTab; onSub: (next: PromoTab) => void }) {
  const toast = useToast();
  const { create, update } = usePromoMutations();
  const [edit, setEdit] = useState<PromoCode | null>(null);

  const save = async (p: Omit<PromoCode, "created_at" | "created_by">) => {
    const body = {
      code: p.code, name: p.name, description: p.description, type: p.type, value: Number(p.value),
      currency: p.type === "fixed" || p.type === "percent" ? p.currency : "",
      starts_at: epoch(p.starts_at), ends_at: epoch(p.ends_at), max_uses: p.max_uses ? Number(p.max_uses) : undefined,
      per_user_limit: Number(p.per_user_limit) || 1, discount_ttl: Number(p.discount_ttl) || 0,
      min_order: Number(p.min_order) || 0, max_discount: Number(p.max_discount) || 0, tariff_ids: p.tariff_ids,
      pool_id: p.type === "traffic" ? p.pool_id : undefined,
      first_purchase_only: p.first_purchase_only, new_users_only: p.new_users_only, enabled: p.enabled,
    };
    if (p.id) await update.mutateAsync({ id: p.id, body });
    else await create.mutateAsync(body);
    toast.ok(t("promocodes.saved"));
    setEdit(null);
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Segmented label={t("promocodes.sections")} value={sub} onChange={onSub} options={PROMO_TABS.map((id) => ({ value: id, label: t(`promocodes.${id}`) }))} />
        <Button variant="primary" size="sm" onClick={() => setEdit({ ...empty })}>
          <Plus size={16} aria-hidden />
          {t("promocodes.create")}
        </Button>
      </div>
      {sub === "codes" ? <Codes onEdit={setEdit} onCreate={() => setEdit({ ...empty })} /> : <Redemptions />}
      <PromoEditor value={edit} onClose={() => setEdit(null)} onSave={save} />
    </div>
  );
}

function Codes({ onEdit, onCreate }: { onEdit: (p: PromoCode) => void; onCreate: () => void }) {
  const toast = useToast();
  const codes = usePromocodes();
  const { toggle, remove } = usePromoMutations();
  const [removing, setRemoving] = useState<PromoCode | null>(null);
  const items = codes.data?.items ?? [];
  return (
    <section className="card glass reveal" aria-busy={codes.isFetching}>
      {codes.isError && !codes.data ? (
        <ErrorState text={errorText(codes.error)} onRetry={() => void codes.refetch()} />
      ) : codes.isPending ? (
        <div className="flex flex-col gap-2" role="status" aria-busy aria-label={t("common.loading")}>
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} style={{ height: 56 }} />
          ))}
        </div>
      ) : items.length === 0 ? (
        <EmptyState title={t("promocodes.empty")} text={t("promocodes.emptyText")}>
          <Button variant="primary" onClick={onCreate}>
            <Plus size={18} aria-hidden /> {t("promocodes.create")}
          </Button>
        </EmptyState>
      ) : (
        <ul className="row-list">
          {items.map((p) => (
            <li key={p.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 py-3">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <b className="mono text-[13px]">{p.code}</b>
                  <Pill tone={statusTone(p.status)}>{statusText(p.status)}</Pill>
                  <span className="text-[13px] font-medium">{valueText(p)}</span>
                </div>
                <div className="mt-1 truncate text-xs text-[var(--ink-500)]">
                  {[p.name || p.description, t("promocodes.usedOf", { n: p.used_count, max: p.max_uses ? String(p.max_uses) : "∞" })].filter(Boolean).join(" · ")}
                </div>
              </div>
              <div className="flex items-center gap-1">
                <Switch
                  checked={p.enabled}
                  label={t("promocodes.toggleLabel", { code: p.code })}
                  disabled={toggle.isPending && toggle.variables?.id === p.id}
                  onChange={(enabled) => toggle.mutate({ id: p.id, enabled }, { onError: (e) => toast.error(errorText(e)) })}
                />
                <button type="button" className="icon-btn ml-2" aria-label={t("promocodes.editLabel", { code: p.code })} title={t("promocodes.edit")} onClick={() => onEdit(p)}>
                  <Pencil size={16} aria-hidden />
                </button>
                <button type="button" className="icon-btn" aria-label={t("promocodes.deleteLabel", { code: p.code })} title={t("common.delete")} onClick={() => setRemoving(p)}>
                  <Trash2 size={16} aria-hidden />
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <Confirm
        open={!!removing}
        onOpenChange={(v) => !v && setRemoving(null)}
        title={t("promocodes.deleteTitle", { code: removing?.code ?? "" })}
        text={t("promocodes.deleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() =>
          removing &&
          remove.mutate(removing.id, {
            onSuccess: () => setRemoving(null),
            onError: (e) => toast.error(errorText(e)),
          })
        }
      />
    </section>
  );
}

function Redemptions() {
  const history = usePromoRedemptions();
  const [search, setSearch] = useState("");
  const list = useMemo(() => {
    const q = search.trim().toLowerCase();
    return (history.data?.items ?? []).filter((r: PromoRedemption) => !q || r.code.toLowerCase().includes(q) || String(r.user_id ?? "").includes(q));
  }, [history.data, search]);
  return (
    <section className="card glass reveal">
      <label className="search-field mb-4 max-w-[420px]">
        <Search size={16} aria-hidden />
        <input type="search" placeholder={t("promocodes.historySearch")} value={search} onChange={(e) => setSearch(e.target.value)} aria-label={t("promocodes.historySearch")} />
      </label>
      {history.isError && !history.data ? (
        <ErrorState text={errorText(history.error)} onRetry={() => void history.refetch()} />
      ) : history.isPending ? (
        <div className="flex flex-col gap-2" role="status" aria-busy aria-label={t("common.loading")}>
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} style={{ height: 48 }} />
          ))}
        </div>
      ) : list.length === 0 ? (
        <EmptyState search={!!search.trim()} title={t("promocodes.noHistory")} text={search.trim() ? t("promocodes.noHistoryFound") : t("promocodes.noHistoryText")} />
      ) : (
        <ul className="row-list">
          {list.map((r) => (
            <li key={r.id} className="py-3">
              <div className="flex flex-wrap items-center gap-2 text-[13px]">
                <b className="mono">{r.code}</b>
                <span className="text-[var(--ink-500)]">{redemptionStatusText(r.status)}</span>
                {r.days ? <span>+{r.days} {t("promocodes.daysUnit")}</span> : null}
                {r.bytes ? (
                  <span>
                    +{(r.bytes / GB).toFixed(1)} {t("promocodes.trafficUnit")}
                  </span>
                ) : null}
                {r.discount_amount ? <span>−{money(r.discount_amount, r.currency)}</span> : null}
              </div>
              <div className="mt-1 text-xs text-[var(--ink-500)]">
                {dateShort(r.redeemed_at)} {time(r.redeemed_at)} · {r.user_id != null ? `${t("promocodes.user")} #${r.user_id}` : t("promocodes.noUser")} · {t("promocodes.telegramId")} {r.tg_id}
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** A promo code in a side sheet: what it gives first, who may use it and when folded below. */
function PromoEditor({ value, onClose, onSave }: { value: PromoCode | null; onClose: () => void; onSave: (p: Omit<PromoCode, "created_at" | "created_by">) => Promise<void> }) {
  const [saving, setSaving] = useState(false);
  const [blocked, setBlocked] = useState(false);
  return (
    <Drawer
      open={!!value}
      onOpenChange={(v) => !v && onClose()}
      title={value?.id ? t("promocodes.edit") : t("promocodes.new")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" type="submit" form="promo-form" loading={saving} disabled={blocked}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      {value ? <PromoForm key={value.id || "new"} value={value} onSave={onSave} onSaving={setSaving} onBlocked={setBlocked} /> : null}
    </Drawer>
  );
}

function PromoForm({ value, onSave, onSaving, onBlocked }: { value: PromoCode; onSave: (p: Omit<PromoCode, "created_at" | "created_by">) => Promise<void>; onSaving: (v: boolean) => void; onBlocked: (v: boolean) => void }) {
  const pools = usePools();
  const tariffs = useTariffs();
  const [p, setP] = useState<Draft>(() => ({
    ...value,
    value: value.type === "traffic" ? value.value / GB : value.type === "fixed" ? fromUnits(value.value, value.currency) : value.value,
    min_order: fromUnits(value.min_order, value.currency),
    max_discount: fromUnits(value.max_discount, value.currency),
    starts_at: dateInput(value.starts_at),
    ends_at: dateInput(value.ends_at),
    max_uses: value.max_uses,
    tariff_ids: [...value.tariff_ids],
  }));
  const [error, setError] = useState("");
  const [dateErrors, setDateErrors] = useState<{ starts_at?: string; ends_at?: string }>({});
  const set = (key: keyof Draft) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setP((x) => ({ ...x, [key]: e.target.value }));
  const flag = (key: "first_purchase_only" | "new_users_only" | "enabled") => (on: boolean) => setP((x) => ({ ...x, [key]: on }));
  // What the form needs to be saved is missing: the sheet's save button waits.
  const blocked = tariffs.isError || (p.type === "traffic" && pools.isError);
  useEffect(() => onBlocked(blocked), [blocked, onBlocked]);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    const dates = {
      starts_at: badDate(p.starts_at) ? t("promocodes.badDate") : undefined,
      ends_at: badDate(p.ends_at) ? t("promocodes.badDate") : undefined,
    };
    setDateErrors(dates);
    if (dates.starts_at || dates.ends_at) return;
    onSaving(true);
    try {
      await onSave({
        ...p,
        value: p.type === "traffic" ? Math.round(Number(p.value) * GB) : p.type === "fixed" ? toUnits(p.value, p.currency) : Number(p.value),
        per_user_limit: Number(p.per_user_limit),
        discount_ttl: Number(p.discount_ttl),
        // Sums mean nothing without a currency: a code for any currency has none.
        min_order: p.currency ? toUnits(p.min_order, p.currency) : 0,
        max_discount: p.currency ? toUnits(p.max_discount, p.currency) : 0,
        max_uses: p.max_uses ? Number(p.max_uses) : undefined,
        tariff_ids: p.tariff_ids,
        starts_at: p.starts_at || undefined,
        ends_at: p.ends_at || undefined,
      });
    } catch (err) {
      setError(errorText(err));
    } finally {
      onSaving(false);
    }
  };
  const discount = p.type === "percent" || p.type === "fixed";
  const step = p.type === "fixed" && p.currency === "RUB" ? "0.01" : "1";
  return (
    <form id="promo-form" className="pt-4" onSubmit={(e) => void submit(e)} noValidate>
      <p className="mb-4 text-[13px] text-[var(--ink-500)]">{t("promocodes.createHint")}</p>
      {error ? (
        <div className="banner err mb-4" role="alert">
          {error}
        </div>
      ) : null}
      <Field label={t("promocodes.code")} htmlFor="pc-code">
        <input id="pc-code" className="input mono" value={p.code} onChange={set("code")} maxLength={64} autoComplete="off" spellCheck={false} />
      </Field>
      <div className="grid gap-x-3 sm:grid-cols-2">
        <Field label={t("promocodes.type")} htmlFor="pc-type">
          <select id="pc-type" className="input" value={p.type} onChange={set("type")}>
            <option value="days">{t("promocodes.days")}</option>
            <option value="traffic">{t("promocodes.traffic")}</option>
            <option value="percent">{t("promocodes.percent")}</option>
            <option value="fixed">{t("promocodes.fixed")}</option>
          </select>
        </Field>
        <Field label={p.type === "fixed" && p.currency ? `${t("promocodes.value")}, ${unitOf(p.currency)}` : t("promocodes.value")} htmlFor="pc-value">
          <input id="pc-value" className="input" type="number" inputMode="decimal" min={step === "0.01" ? "0.01" : "1"} step={step} max={p.type === "days" ? 36500 : undefined} value={p.value} onChange={set("value")} />
        </Field>
      </div>
      {discount ? (
        <Field label={t("promocodes.currency")} htmlFor="pc-currency" hint={p.type === "percent" && !p.currency ? t("promocodes.anyCurrencyHint") : undefined}>
          <select id="pc-currency" className="input" value={p.currency} onChange={set("currency")}>
            {p.type === "percent" || !p.currency ? <option value="">{t("promocodes.anyCurrency")}</option> : null}
            <option value="RUB">RUB</option>
            <option value="XTR">XTR</option>
          </select>
        </Field>
      ) : null}
      {p.type === "traffic" ? (
        <Field label={t("promocodes.trafficPool")} htmlFor="pc-pool" error={pools.isError ? errorText(pools.error) : undefined}>
          <select id="pc-pool" className="input" value={p.pool_id ?? ""} disabled={pools.isError} onChange={(e) => setP((x) => ({ ...x, pool_id: e.target.value ? Number(e.target.value) : undefined }))}>
            <option value="">{t("promocodes.mainTraffic")}</option>
            {(pools.data ?? []).map((pool) => (
              <option key={pool.id} value={pool.id}>
                {pool.name}
              </option>
            ))}
          </select>
        </Field>
      ) : null}
      <Field label={t("promocodes.name")} htmlFor="pc-name">
        <input id="pc-name" className="input" value={p.name} onChange={set("name")} />
      </Field>
      <SwitchRow label={t("promocodes.enabled")} checked={p.enabled} onChange={flag("enabled")} />
      <Disclosure title={t("promocodes.limits")} sub={t("promocodes.limitsSub")}>
        <div className="grid gap-x-3 sm:grid-cols-2">
          <Field label={t("promocodes.startsAt")} htmlFor="pc-start" error={dateErrors.starts_at}>
            <input
              id="pc-start"
              className="input"
              type="datetime-local"
              min={DATE_MIN}
              max={DATE_MAX}
              value={p.starts_at}
              onChange={set("starts_at")}
              aria-invalid={!!dateErrors.starts_at}
            />
          </Field>
          <Field label={t("promocodes.endsAt")} htmlFor="pc-end" error={dateErrors.ends_at}>
            <input
              id="pc-end"
              className="input"
              type="datetime-local"
              min={DATE_MIN}
              max={DATE_MAX}
              value={p.ends_at}
              onChange={set("ends_at")}
              aria-invalid={!!dateErrors.ends_at}
            />
          </Field>
          <Field label={t("promocodes.maxUses")} htmlFor="pc-max">
            <input id="pc-max" className="input" type="number" min="1" placeholder={t("promocodes.noLimit")} value={p.max_uses ?? ""} onChange={set("max_uses")} />
          </Field>
          <Field label={t("promocodes.perUserLimit")} htmlFor="pc-per-user">
            <input id="pc-per-user" className="input" type="number" min="1" value={p.per_user_limit} onChange={set("per_user_limit")} />
          </Field>
        </div>
        {discount && p.currency ? (
          <div className="grid gap-x-3 sm:grid-cols-2">
            <Field label={`${t("promocodes.minOrder")}, ${unitOf(p.currency)}`} htmlFor="pc-min" hint={t("promocodes.zeroNoLimit")}>
              <input id="pc-min" className="input" type="number" min="0" step={p.currency === "RUB" ? "0.01" : "1"} value={p.min_order} onChange={set("min_order")} />
            </Field>
            <Field label={`${t("promocodes.maxDiscount")}, ${unitOf(p.currency)}`} htmlFor="pc-max-discount" hint={t("promocodes.zeroNoLimit")}>
              <input id="pc-max-discount" className="input" type="number" min="0" step={p.currency === "RUB" ? "0.01" : "1"} value={p.max_discount} onChange={set("max_discount")} />
            </Field>
          </div>
        ) : null}
        {discount ? (
          <Field label={t("promocodes.discountTtl")} htmlFor="pc-ttl" hint={t("promocodes.discountTtlHint")}>
            <input id="pc-ttl" className="input max-w-[200px]" type="number" min="0" max={30 * 24 * 60 * 60} value={p.discount_ttl} onChange={set("discount_ttl")} />
          </Field>
        ) : null}
        <Field label={t("promocodes.tariffsLabel")} hint={t("promocodes.allTariffsHint")}>
          {tariffs.isError ? (
            <p className="text-[13px] text-[var(--berry-600)]" role="alert">
              {errorText(tariffs.error)}
            </p>
          ) : (
            <div className="panel-soft flex max-h-40 flex-col gap-2 overflow-auto p-3">
              {(tariffs.data ?? []).map((tariff) => (
                <label key={tariff.id} className="flex items-center gap-2 text-[13px]">
                  <input
                    type="checkbox"
                    className="check"
                    checked={p.tariff_ids.includes(tariff.id)}
                    onChange={(e) => setP((x) => ({ ...x, tariff_ids: e.target.checked ? [...x.tariff_ids, tariff.id] : x.tariff_ids.filter((id) => id !== tariff.id) }))}
                  />
                  {tariff.name}
                </label>
              ))}
            </div>
          )}
        </Field>
        <div className="row-list">
          <SwitchRow label={t("promocodes.firstPurchaseOnly")} checked={p.first_purchase_only} onChange={flag("first_purchase_only")} />
          <SwitchRow label={t("promocodes.newUsersOnly")} checked={p.new_users_only} onChange={flag("new_users_only")} />
        </div>
        <Field label={t("promocodes.description")} htmlFor="pc-description">
          <input id="pc-description" className="input" value={p.description} onChange={set("description")} />
        </Field>
      </Disclosure>
    </form>
  );
}
