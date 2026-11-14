// A node's server at its provider: who hosts it, when it is paid for and the plan's traffic.
// The panel only reminds the admin (Telegram → infrastructure alerts), it pays and limits nothing.
import type { Schemas } from "../../../api/client";
import { Bar, Field, Pill, Segmented } from "../../../components/ui";
import { t } from "../../../i18n";
import { bytes, dateLong, daysUntil, GiB, months } from "../../../lib/format";
import type { Node } from "../nodes";

type Billing = Schemas["NodeBilling"];

/** The billing as the form edits it: the numbers as typed, the limit in GB. */
export type BillingForm = { provider: string; pay_date: string; pay_months: number; remind_days: string; limit_gb: string; reset_day: string; alert: string };

export const billingForm = (b: Billing): BillingForm => ({
  provider: b.provider,
  pay_date: b.pay_date,
  pay_months: b.pay_months,
  remind_days: String(b.remind_days),
  limit_gb: b.traffic_limit ? String(Math.round((b.traffic_limit / GiB) * 100) / 100) : "",
  reset_day: String(b.reset_day),
  alert: String(b.traffic_alert),
});

/** The billing to send, or the errors of the fields that are wrong (keyed as the API keys them). */
export function billingBody(f: BillingForm): { body: Billing } | { errors: Record<string, string> } {
  const int = (s: string, lo: number, hi: number) => {
    const n = Number(s.trim());
    return s.trim() !== "" && Number.isInteger(n) && n >= lo && n <= hi ? n : null;
  };
  const errors: Record<string, string> = {};
  const remind = int(f.remind_days, 0, 60);
  if (remind === null) errors["billing.remind_days"] = t("nodes.billingRemindErr");
  const gb = f.limit_gb.trim() === "" ? 0 : Number(f.limit_gb.trim().replace(",", "."));
  if (!Number.isFinite(gb) || gb < 0 || gb > 1e9) errors["billing.traffic_limit"] = t("nodes.billingLimitErr");
  const reset = int(f.reset_day, 1, 31);
  if (reset === null) errors["billing.reset_day"] = t("nodes.billingResetErr");
  const alert = int(f.alert, 1, 100);
  if (alert === null) errors["billing.traffic_alert"] = t("nodes.billingAlertErr");
  if (Object.keys(errors).length > 0 || remind === null || reset === null || alert === null) return { errors };
  return { body: { provider: f.provider.trim(), pay_date: f.pay_date, pay_months: f.pay_months, remind_days: remind, traffic_limit: Math.round(gb * GiB), reset_day: reset, traffic_alert: alert } };
}

const PERIODS = [0, 1, 3, 6, 12];

/** The form's section of the server's billing. */
export function BillingFields({ form, onChange, errors }: { form: BillingForm; onChange: (patch: Partial<BillingForm>) => void; errors: Record<string, string> }) {
  const periods = PERIODS.includes(form.pay_months) ? PERIODS : [...PERIODS, form.pay_months].sort((a, b) => a - b);
  const digits = (k: "remind_days" | "reset_day" | "alert") => (e: React.ChangeEvent<HTMLInputElement>) => onChange({ [k]: e.target.value.replace(/[^0-9]/g, "") });
  return (
    <div className="dr-sec">
      <div className="switch-row-title mb-1">{t("nodes.billing")}</div>
      <div className="switch-row-sub mb-3">{t("nodes.billingSub")}</div>
      <Field label={t("nodes.billingProvider")} htmlFor="e-billing-provider" error={errors["billing.provider"]}>
        <input id="e-billing-provider" className="input" value={form.provider} onChange={(e) => onChange({ provider: e.target.value })} maxLength={100} placeholder="Hetzner" autoComplete="off" />
      </Field>
      <div className="grid gap-x-3 sm:grid-cols-[1fr_160px]">
        <Field label={t("nodes.billingPayDate")} htmlFor="e-billing-date" hint={t("nodes.billingPayDateHint")} error={errors["billing.pay_date"]}>
          <input id="e-billing-date" type="date" className="input min-w-0" value={form.pay_date} max="9999-12-31" onChange={(e) => onChange({ pay_date: e.target.value })} />
        </Field>
        <Field label={t("nodes.billingRemind")} htmlFor="e-billing-remind" hint={t("nodes.billingRemindHint")} error={errors["billing.remind_days"]}>
          <label className="input-unit">
            <input id="e-billing-remind" className="input" inputMode="numeric" value={form.remind_days} onChange={digits("remind_days")} aria-invalid={!!errors["billing.remind_days"]} />
            <span>{t("nodes.billingDaysUnit")}</span>
          </label>
        </Field>
      </div>
      <Field label={t("nodes.billingRepeat")}>
        <Segmented
          label={t("nodes.billingRepeat")}
          value={String(form.pay_months)}
          onChange={(v) => onChange({ pay_months: Number(v) })}
          options={periods.map((m) => ({ value: String(m), label: m === 0 ? t("nodes.billingOnce") : months(m) }))}
        />
      </Field>
      <div className="grid gap-x-3 sm:grid-cols-3">
        <Field label={t("nodes.billingLimit")} htmlFor="e-billing-limit" hint={t("nodes.billingLimitHint")} error={errors["billing.traffic_limit"]}>
          <label className="input-unit">
            <input id="e-billing-limit" className="input" inputMode="decimal" value={form.limit_gb} onChange={(e) => onChange({ limit_gb: e.target.value.replace(/[^0-9.,]/g, "") })} placeholder="∞" aria-invalid={!!errors["billing.traffic_limit"]} />
            <span>{t("units.gb")}</span>
          </label>
        </Field>
        <Field label={t("nodes.billingReset")} htmlFor="e-billing-reset" hint={t("nodes.billingResetHint")} error={errors["billing.reset_day"]}>
          <input id="e-billing-reset" className="input" inputMode="numeric" value={form.reset_day} onChange={digits("reset_day")} disabled={!form.limit_gb.trim()} aria-invalid={!!errors["billing.reset_day"]} />
        </Field>
        <Field label={t("nodes.billingAlert")} htmlFor="e-billing-alert" hint={t("nodes.billingAlertHint")} error={errors["billing.traffic_alert"]}>
          <label className="input-unit">
            <input id="e-billing-alert" className="input" inputMode="numeric" value={form.alert} onChange={digits("alert")} disabled={!form.limit_gb.trim()} aria-invalid={!!errors["billing.traffic_alert"]} />
            <span>%</span>
          </label>
        </Field>
      </div>
    </div>
  );
}

/** A UTC day ("2026-11-02") as a moment the local calendar shows as that day too. */
const noon = (day: string) => `${day}T12:00:00Z`;

/** The card's rows of the server's billing: the next payment and the plan's traffic; nothing when none is set. */
export function BillingRows({ n }: { n: Node }) {
  const b = n.billing;
  const v = n.vps_traffic;
  const left = n.next_payment ? daysUntil(noon(n.next_payment)) : null;
  return (
    <>
      {n.next_payment || b.provider ? (
        <div className="col-span-2">
          <dt className="text-xs text-[var(--ink-500)]">{t("nodes.billingPayment")}</dt>
          <dd className="flex flex-wrap items-center gap-2">
            <span>{[b.provider, n.next_payment ? dateLong(noon(n.next_payment)) : ""].filter(Boolean).join(" · ")}</span>
            {left !== null && left <= Math.max(b.remind_days, 0) ? <Pill tone="warn">{left === 0 ? t("time.today") : t("nodes.billingIn", { n: left })}</Pill> : null}
          </dd>
        </div>
      ) : null}
      {v ? (
        <div className="col-span-2">
          <dt className="text-xs text-[var(--ink-500)]">{t("nodes.billingTraffic")}</dt>
          <dd>
            <div className="flex items-center gap-2">
              <Bar pct={(v.used / b.traffic_limit) * 100} className="flex-1" label={t("nodes.billingTraffic")} />
              <span className="num w-10 text-right">{Math.floor((v.used / b.traffic_limit) * 100)}%</span>
            </div>
            <div className="num mt-1 text-xs text-[var(--ink-500)]">{t("nodes.billingTrafficOf", { used: bytes(v.used), limit: bytes(b.traffic_limit), date: dateLong(noon(v.resets)) })}</div>
          </dd>
        </div>
      ) : null}
    </>
  );
}
