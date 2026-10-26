import { Check, Play } from "lucide-react";
import { errorText, type Schemas, type User } from "../../../api/client";
import { userActions, useUserMutation } from "../../../api/hooks";
import { useToast } from "../../../components/toast";
import { Button, Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { dateLong, dateShort, days, expiryText, fromInputDate, inputDate, months, waitingDays } from "../../../lib/format";
import { Section } from "./section";
import { Select } from "../../../components/select";

export function ExpirySection({ u }: { u: User }) {
  const toast = useToast();
  const extend = useUserMutation(userActions.extend);
  const update = useUserMutation(userActions.update);
  const e = expiryText(u.expires_at);
  const fail = (x: unknown) => toast.error(errorText(x));
  // With a billing day a term runs from that day to the same day: extend by months.
  const chips: { label: string; body: Schemas["ExtendInputBody"] }[] =
    u.billing_day != null
      ? [1, 3, 6, 12].map((n) => ({ label: n === 12 ? t("userDrawer.year") : months(n), body: { months: n } }))
      : [7, 30, 90, 365].map((n) => ({ label: n === 365 ? t("userDrawer.year") : days(n), body: { days: n } }));
  return (
    <Section title={t("userDrawer.expiry")} aside={u.billing_day != null ? t("userDrawer.billingAside", { d: u.billing_day }) : undefined}>
      {u.starts_on_connect && u.expires_at ? (
        <WaitingTerm u={u} />
      ) : (
        <>
          <div className="font-display text-lg font-medium tracking-tight">{u.expires_at ? t("users.until", { date: dateLong(u.expires_at) }) : t("userDrawer.forever")}</div>
          {u.expires_at ? <div className={`exp-days ${e.tone}`}>{e.tone === "bad" ? e.text : t("userDrawer.leftDays", { text: e.text })}</div> : null}
          <WaitOption u={u} />
        </>
      )}
      <div className="mt-3 flex flex-wrap gap-2">
        {chips.map((c) => (
          <button
            key={c.label}
            type="button"
            className="chip-btn"
            disabled={extend.isPending}
            onClick={() => extend.mutate({ id: u.id, ...c.body }, { onSuccess: (r) => toast.ok(t("userDrawer.extendedUntil", { date: dateShort(r.expires_at!) })), onError: fail })}
          >
            +{c.label}
          </button>
        ))}
        {u.expires_at ? (
          <button type="button" className="chip-btn" disabled={update.isPending} onClick={() => update.mutate({ id: u.id, body: { never_expires: true } }, { onSuccess: () => toast.ok(t("userDrawer.nowForever")), onError: fail })}>
            {t("userDrawer.forever")}
          </button>
        ) : null}
      </div>
      <div className="mt-4 grid grid-cols-1 gap-x-3 sm:grid-cols-2">
        <BillingDayField u={u} />
        <ExactDateField u={u} />
      </div>
    </Section>
  );
}

/** A term that waits for its first connection: its whole length, kept until then, and the
 * admin's way to start it now. */
function WaitingTerm({ u }: { u: User }) {
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  return (
    <div className="panel-soft flex flex-wrap items-center justify-between gap-3 p-4">
      <div className="min-w-0">
        <div className="font-display text-lg font-medium tracking-tight">{t("userDrawer.waitTitle", { term: days(waitingDays(u.expires_at!)) })}</div>
        <p className="mt-1 text-xs text-[var(--ink-500)]">{t("userDrawer.waitText")}</p>
      </div>
      <Button
        size="sm"
        loading={update.isPending}
        onClick={() =>
          update.mutate(
            { id: u.id, body: { starts_on_connect: false } },
            { onSuccess: (r) => toast.ok(t("userDrawer.waitStarted", { date: dateShort(r.expires_at!) })), onError: (x) => toast.error(errorText(x)) },
          )
        }
      >
        <Play size={16} aria-hidden /> {t("userDrawer.waitStart")}
      </Button>
    </div>
  );
}

/** A subscription that never connected and has its term ahead may wait for its first
 * connection: the admin made it, the client has not set the app up yet. */
function WaitOption({ u }: { u: User }) {
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  const never = !u.online_at && u.total_up + u.total_down === 0;
  if (!never || !u.expires_at || new Date(u.expires_at).getTime() <= Date.now()) return null;
  return (
    <button
      type="button"
      className="link-btn mt-2 text-xs"
      disabled={update.isPending}
      onClick={() =>
        update.mutate({ id: u.id, body: { starts_on_connect: true } }, { onSuccess: () => toast.ok(t("userDrawer.waitSet")), onError: (x) => toast.error(errorText(x)) })
      }
    >
      {t("userDrawer.waitOffer")}
    </button>
  );
}

function BillingDayField({ u }: { u: User }) {
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  const set = (d: number) =>
    update.mutate(
      { id: u.id, body: { billing_day: d } },
      { onSuccess: () => toast.ok(d ? t("userDrawer.billingDaySet", { d }) : t("userDrawer.billingDayCleared")), onError: (e) => toast.error(errorText(e)) },
    );
  return (
    <Field label={t("userDrawer.billingDay")} htmlFor={`u-bday-${u.id}`} hint={t("userDrawer.billingDayHint")}>
      <Select id={`u-bday-${u.id}`} className="input" value={u.billing_day ?? 0} disabled={update.isPending} onChange={(e) => set(Number(e.target.value))}>
        <option value={0}>{t("userDrawer.billingDayNone")}</option>
        {Array.from({ length: 31 }, (_, i) => i + 1).map((d) => (
          <option key={d} value={d}>
            {t("userDrawer.billingDayOption", { d })}
          </option>
        ))}
      </Select>
    </Field>
  );
}

function ExactDateField({ u }: { u: User }) {
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  const current = u.expires_at ? inputDate(u.expires_at) : "";
  const { draft: date, setDraft: setDate } = useDraft(current);
  // Chrome takes a six-digit year unless max caps it; such a date is not saved.
  const valid = /^\d{4}-\d{2}-\d{2}$/.test(date) && Number(date.slice(0, 4)) <= 9999;
  const apply = () =>
    update.mutate(
      { id: u.id, body: { expires_at: fromInputDate(date, u.expires_at) } },
      { onSuccess: (r) => toast.ok(t("userDrawer.extendedUntil", { date: dateShort(r.expires_at!) })), onError: (e) => toast.error(errorText(e)) },
    );
  return (
    <Field label={t("userDrawer.exactDate")} htmlFor={`u-date-${u.id}`} hint={t("userDrawer.exactDateHint")}>
      <div className="flex items-center gap-2">
        <input id={`u-date-${u.id}`} type="date" className="input min-w-0" value={date} min={inputDate(new Date().toISOString())} max="9999-12-31" onChange={(e) => setDate(e.target.value)} />
        {valid && date !== current ? (
          <Button variant="primary" className="h-11 w-11 shrink-0 px-0" loading={update.isPending} onClick={apply} aria-label={t("common.save")} title={t("common.save")}>
            {update.isPending ? null : <Check size={18} aria-hidden />}
          </Button>
        ) : null}
      </div>
    </Field>
  );
}
