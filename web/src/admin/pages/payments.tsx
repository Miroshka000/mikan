import { keepPreviousData, useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Coins, HandCoins, History as HistoryIcon, ListChecks, Megaphone, Ticket, TriangleAlert, Undo2, Wallet } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";
import { api, apiCodeText, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useAddons, usePaymentSettings, useTariffs, useTelegram } from "../../api/hooks";
import { Confirm } from "../../components/overlay";
import { useToast } from "../../components/toast";
import { QueryBoundary } from "../../components/query";
import { Switch, SwitchRow } from "../../components/switch";
import { CardHead, FormActions } from "../../components/layout";
import { Tabs } from "../../components/tabs";
import { Button, EmptyState, Field, PageHeader, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useDraft } from "../../lib/draft";
import { dateShort, days, money, num, time } from "../../lib/format";
import { isKopecks, isStars, rubKopecks } from "../../lib/validate";
import { PAYMENT_TABS, type PaymentsSearch } from "../search";
import { addonName } from "./payment-addons";
import { PromoSection } from "./promocodes";
import { CampaignsSection } from "./campaigns";
import { TopupsSection } from "./topups";
import { Select } from "../../components/select";

type Settings = Schemas["PaymentSettingsView"];
type Payment = Schemas["PaymentView"];
type Status = Payment["status"];
type Provider = Payment["provider"];
type Addons = Schemas["AddonsView"];

const STATUS_TONE: Record<Status, "ok" | "warn" | "bad" | "off"> = {
  applied: "ok",
  paid: "warn",
  pending: "off",
  expired: "off",
  failed: "bad",
  refunded: "bad",
};
const STATUSES: Status[] = ["applied", "paid", "pending", "failed", "expired", "refunded"];
const BUILT_IN: Provider[] = ["stars"];
const TAB_ICONS = { history: HistoryIcon, topups: Coins, methods: Wallet, rules: ListChecks, promocodes: Ticket, campaigns: Megaphone } as const;
type PaymentTab = NonNullable<PaymentsSearch["tab"]>;

/** A payment's provider as the admin knows it; adapters by their own name. */
function providerName(p: Provider, addons: Addons | undefined): string {
  return p.startsWith("addon:") ? addonName(p.slice("addon:".length), addons) : t(`payments.providers.${p}` as "payments.providers.stars");
}

export function PaymentsPage() {
  const settings = usePaymentSettings();
  const { tab, promo } = useSearch({ from: "/_app/payments" });
  const navigate = useNavigate({ from: "/payments" });
  const s = settings.data;
  // Until a way to take payments is on, setting one up is what the page is for.
  const ready = !!s && s.enabled && (s.available.stars || s.available.addons.length > 0 || s.available.balance);
  const shown: PaymentTab = tab ?? (s && !ready ? "methods" : "history");
  return (
    <>
      <PageHeader title={t("payments.title")} sub={t("payments.subtitle")} actions={<SellingSwitch s={s} />} />
      {s && !s.enabled ? (
        <div className="banner warn" role="status">
          <TriangleAlert size={16} className="shrink-0" aria-hidden />
          <span className="min-w-0 flex-1">{t("payments.salesOff")}</span>
        </div>
      ) : null}
      {s && (s.available.stars || s.available.addons.length > 0) && s.on_sale === 0 ? (
        <div className="banner warn flex-wrap" role="status">
          <span className="min-w-0 flex-1">{t("payments.nothingOnSale")}</span>
          <Link to="/tariffs" search={{ tab: "tariffs" }} className="btn btn-glass btn-sm">
            {t("payments.openTariffs")}
          </Link>
        </div>
      ) : null}
      <Tabs
        id="payments"
        label={t("payments.sections")}
        tabs={PAYMENT_TABS.map((id) => ({ id, label: t(`payments.tabs.${id}`), icon: TAB_ICONS[id] }))}
        value={shown}
        onChange={(next) => void navigate({ search: { tab: next }, replace: true })}
      >
        {shown === "history" ? (
          <History />
        ) : shown === "topups" ? (
          <TopupsSection />
        ) : shown === "campaigns" ? (
          <CampaignsSection />
        ) : shown === "promocodes" ? (
          <PromoSection sub={promo ?? "codes"} onSub={(next) => void navigate({ search: { tab: "promocodes", promo: next }, replace: true })} />
        ) : (
          <QueryBoundary query={settings} pending={<Skeleton style={{ height: 320, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
            {(v) =>
              shown === "methods" ? (
                // Stars are built in; the other payment methods are addons, set up in Addons.
                <div className="grid grid-cols-[minmax(0,1fr)] gap-4 lg:grid-cols-2 lg:items-start">
                  <StarsCard s={v} />
                  <AddonMethodsCard />
                  <BalanceCard s={v} />
                </div>
              ) : (
                <div className="flex max-w-[960px] flex-col gap-4">
                  <RulesCard s={v} />
                </div>
              )
            }
          </QueryBoundary>
        )}
      </Tabs>
    </>
  );
}

/** Selling on or off for the whole panel: the bot and the Mini App sell only while it is on. */
function SellingSwitch({ s }: { s?: Settings }) {
  const qc = useQueryClient();
  const toast = useToast();
  const save = useMutation({
    mutationFn: (enabled: boolean) => unwrap(api.PATCH("/api/v1/payments/settings", { body: { enabled } })),
    onSuccess: (v) => {
      qc.setQueryData(qk.paymentSettings, v);
      toast.ok(v.enabled ? t("settings.salesOnToast") : t("settings.salesOffToast"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  if (!s) return <Skeleton style={{ width: 160, height: 40, borderRadius: 12 }} />;
  return (
    <div className="flex h-10 items-center gap-3 rounded-[var(--r-md)] border border-[var(--hairline)] bg-[var(--surface)] px-3">
      <span className="text-[13px] font-medium">{t("settings.sales")}</span>
      <Switch checked={s.enabled} label={t("settings.sales")} disabled={save.isPending} onChange={(on) => save.mutate(on)} />
    </div>
  );
}

function History() {
  const qc = useQueryClient();
  const toast = useToast();
  const [status, setStatus] = useState<Status | "">("");
  const [provider, setProvider] = useState<Provider | "">("");
  const [refund, setRefund] = useState<Payment | null>(null);
  const [byHand, setByHand] = useState<Payment | null>(null);
  const addons = useAddons().data;
  const providers = [...BUILT_IN, ...(addons?.installed.map((a) => `addon:${a.id}`) ?? [])];
  const list = useInfiniteQuery({
    queryKey: [...qk.payments, status, provider],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) =>
      unwrap(api.GET("/api/v1/payments", { params: { query: { status: status || undefined, provider: provider || undefined, before: pageParam || undefined, limit: 50 } }, signal })),
    getNextPageParam: (last) => (last.items.length === 50 ? last.items[last.items.length - 1]!.id : undefined),
    placeholderData: keepPreviousData,
  });
  const doRefund = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/payments/{id}/refund", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.payments });
      setRefund(null);
      toast.ok(t("payments.refunded"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  // A payment paid through an adapter and never applied, refunded by the admin in the
  // provider's dashboard: the panel only records it.
  const doByHand = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/payments/{id}/refunded-by-hand", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.payments });
      setByHand(null);
      toast.ok(t("payments.byHandDone"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const items = list.data?.pages.flatMap((p) => p.items) ?? [];
  const totals = list.data?.pages[0]?.totals ?? [];
  return (
    <section className="card glass reveal min-w-0">
      <CardHead title={t("payments.history")} sub={totals.length ? totals.map((x) => t("payments.total", { n: num(x.count), sum: money(x.total, x.currency) })).join(" · ") : t("payments.noTotals")} />
      <div className="mb-4 flex flex-wrap gap-2">
        <Select className="input max-w-[200px]" value={status} onChange={(e) => setStatus(e.target.value as Status | "")} aria-label={t("payments.status")}>
          <option value="">{t("payments.allStatuses")}</option>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {t(`payments.statuses.${s}`)}
            </option>
          ))}
        </Select>
        <Select className="input max-w-[200px]" value={provider} onChange={(e) => setProvider(e.target.value as Provider | "")} aria-label={t("payments.provider")}>
          <option value="">{t("payments.allProviders")}</option>
          {providers.map((p) => (
            <option key={p} value={p}>
              {providerName(p, addons)}
            </option>
          ))}
        </Select>
      </div>
      <QueryBoundary
        query={list}
        pending={
          <div className="flex flex-col gap-2">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} style={{ height: 56 }} />
            ))}
          </div>
        }
      >
        {() =>
          items.length === 0 ? (
            <EmptyState title={t("payments.empty")} text={status || provider ? t("payments.emptyFiltered") : t("payments.emptyText")} search={!!(status || provider)} />
          ) : (
            <>
              <ul className="row-list" aria-busy={list.isFetching}>
                {items.map((p) => (
                  <PaymentRow key={p.id} p={p} provider={providerName(p.provider, addons)} onRefund={() => setRefund(p)} onByHand={() => setByHand(p)} />
                ))}
              </ul>
              {list.hasNextPage ? (
                <Button className="mt-3" size="sm" loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                  {t("payments.more")}
                </Button>
              ) : null}
            </>
          )
        }
      </QueryBoundary>
      <Confirm
        open={!!refund}
        onOpenChange={(v) => !v && setRefund(null)}
        title={t("payments.refundTitle", { sum: refund ? money(refund.amount, refund.currency) : "" })}
        text={t(refund?.provider === "stars" ? "payments.refundText" : refund?.provider === "balance" ? "payments.refundBalanceText" : "payments.refundAddonText")}
        confirm={t("payments.refund")}
        danger
        loading={doRefund.isPending}
        onConfirm={() => refund && doRefund.mutate(refund.id)}
      />
      <Confirm
        open={!!byHand}
        onOpenChange={(v) => !v && setByHand(null)}
        title={t("payments.byHandTitle", { sum: byHand ? money(byHand.amount, byHand.currency) : "" })}
        text={t(byHand?.status === "applied" ? "payments.byHandAppliedText" : "payments.byHandText")}
        confirm={t("payments.byHand")}
        loading={doByHand.isPending}
        onConfirm={() => byHand && doByHand.mutate(byHand.id)}
      />
    </section>
  );
}

function PaymentRow({ p, provider, onRefund, onByHand }: { p: Payment; provider: string; onRefund: () => void; onByHand: () => void }) {
  const buyer = p.tg_username ? `@${p.tg_username}` : `tg ${p.tg_id}`;
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 py-3">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <b className="num text-[13px]">{money(p.amount, p.currency)}</b>
          <span className="truncate text-[13px]">{p.term_days != null ? `${p.tariff_name} · ${p.term_days ? days(p.term_days) : t("time.forever")}` : p.tariff_name}</span>
          <Pill tone={STATUS_TONE[p.status]}>{t(`payments.statuses.${p.status}`)}</Pill>
        </div>
        <div className="mt-1 text-xs text-[var(--ink-500)]">
          {dateShort(p.created_at)} {time(p.created_at)} · {provider} · {p.kind === "new" ? t("payments.kindNew") : p.kind === "package" ? t("payments.kindPackage") : p.kind === "trial" ? t("payments.kindTrial") : p.kind === "device" ? t("payments.kindDevice") : t("payments.kindRenew")} · {buyer}
          {p.user_id != null ? (
            <>
              {" → "}
              <Link to="/users" search={{ state: "all", q: "", user: p.user_id }} className="link-btn">
                {p.user_name || `#${p.user_id}`}
              </Link>
            </>
          ) : null}
        </div>
        {p.error ? (
          <div className="mt-1 text-xs text-[var(--berry-600)]">
            {t(p.status === "failed" ? "payments.invoiceFailed" : p.error === "trial_refund_manual" ? "payments.notAppliedByHand" : "payments.notApplied", { error: apiCodeText(p.error) })}
          </div>
        ) : null}
      </div>
      {p.status === "applied" || p.status === "paid" ? (
        <div className="flex shrink-0 flex-wrap justify-end gap-1">
          {/* Stars and the balance give the money back from here; an adapter does when it can. */}
          <Button size="sm" variant="ghost" onClick={onRefund}>
            <Undo2 size={16} aria-hidden /> {t("payments.refund")}
          </Button>
          {p.provider.startsWith("addon:") ? (
            <Button size="sm" variant="ghost" onClick={onByHand}>
              <HandCoins size={16} aria-hidden /> {t("payments.byHand")}
            </Button>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

function useSavePayments() {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: (body: Schemas["PatchPaymentSettingsInputBody"]) => unwrap(api.PATCH("/api/v1/payments/settings", { body })),
    onSuccess: (v) => {
      qc.setQueryData(qk.paymentSettings, v);
      toast.ok(t("payments.saved"));
    },
  });
}

/** Telegram Stars, the way to take payments built into the panel; the others are addons. */
function StarsCard({ s }: { s: Settings }) {
  const save = useSavePayments();
  const { draft: stars, setDraft: setStars, dirty, reset } = useDraft(s.stars);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ stars });
  };
  return (
    <section className="card glass reveal">
      <form onSubmit={submit} noValidate>
        {save.error ? <div className="banner err mb-4" role="alert">{errorText(save.error)}</div> : null}
        <Provider title={t("payments.providers.stars")} sub={t("payments.starsSub")} on={stars} onChange={setStars} live={s.available.stars} selling={s.enabled} offline={s.enabled && stars && !s.available.stars ? t("payments.starsBotOff") : ""} />
        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} />
      </form>
    </section>
  );
}

/** The payment methods of the marketplace are addons: what is installed, and the way to them. */
function AddonMethodsCard() {
  const addons = useAddons().data;
  const installed = addons?.installed ?? [];
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div className="min-w-0">
          <h2 className="card-title">{t("payments.addonMethods")}</h2>
          <div className="card-sub">{t("payments.addonMethodsSub")}</div>
        </div>
      </div>
      {!addons ? (
        <Skeleton style={{ height: 20, width: "60%", borderRadius: 8 }} />
      ) : installed.length ? (
        <ul className="flex flex-wrap gap-2" aria-label={t("payments.addonMethods")}>
          {installed.map((a) => (
            <li key={a.id}>
              <span className="tag">{addonName(a.id, addons)}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-[13px] text-[var(--ink-500)]">{t("payments.addonMethodsNone")}</p>
      )}
      <div className="mt-4 flex justify-end">
        <Link to="/addons" className="btn btn-glass btn-sm">
          {t("payments.openAddons")}
        </Link>
      </div>
    </section>
  );
}

/** The rules' form as the settings hold them. A save puts the draft back to it: "199,50"
 * typed is 199.5 there, and the form would stay unsaved after the save. */
const rulesForm = (s: Settings) => ({
  allowNew: s.allow_new,
  resetTraffic: s.renew_resets_traffic,
  keepDevices: s.extra_devices_keep,
  trial: s.trial_tariff_id ?? 0,
  trialStars: s.trial_stars ? String(s.trial_stars) : "",
  trialRub: s.trial_rub ? String(s.trial_rub / 100) : "",
  free: s.free_tariff_id ?? 0,
  freeCondition: s.free_condition,
});

/** Who may buy, what a renewal does to the traffic, the free trial and the free plan. */
function RulesCard({ s }: { s: Settings }) {
  const tariffs = useTariffs();
  const save = useSavePayments();
  // The free plan's channel condition needs the bot's channel (Telegram → Menu and texts).
  const hasChannel = !!useTelegram().data?.config.channel.chat;
  const { draft: form, setDraft: setForm, dirty, reset } = useDraft(rulesForm(s));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const submit = (e: FormEvent) => {
    e.preventDefault();
    // The trial's price as a tariff's: empty is no price, both empty a free trial.
    const errs: Record<string, string> = {};
    const starsN = Number(form.trialStars);
    const rubN = rubKopecks(form.trialRub);
    if (form.trialStars.trim() && !isStars(starsN)) errs.trial_stars = t("tariffs.errStars");
    if (form.trialRub.trim() && !isKopecks(rubN)) errs.trial_rub = t("tariffs.errRub");
    setErrors(errs);
    if (Object.keys(errs).length) return;
    save.mutate(
      {
        allow_new: form.allowNew,
        renew_resets_traffic: form.resetTraffic,
        extra_devices_keep: form.keepDevices,
        trial_tariff_id: form.trial,
        // No trial, no price: a price left in the hidden fields does not come back later.
        trial_stars: form.trial && form.trialStars.trim() ? starsN : 0,
        trial_rub: form.trial && form.trialRub.trim() ? rubN : 0,
        free_tariff_id: form.free,
        // Sent only when changed: a channel removed from the bot since does not block saving the rest.
        ...(form.freeCondition !== s.free_condition ? { free_condition: form.freeCondition } : {}),
      },
      { onSuccess: (v) => setForm(rulesForm(v)) },
    );
  };
  return (
    <section className="card glass reveal">
      <form onSubmit={submit} noValidate>
        <CardHead title={t("payments.tabs.rules")} sub={t("payments.settingsSub")} />
        {save.error ? <div className="banner err mb-4" role="alert">{errorText(save.error)}</div> : null}
        <div className="row-list">
          <SwitchRow label={t("payments.allowNew")} sub={t("payments.allowNewSub")} checked={form.allowNew} onChange={(v) => setForm((f) => ({ ...f, allowNew: v }))} />
          <SwitchRow
            label={t("payments.resetTraffic")}
            sub={form.resetTraffic ? t("payments.resetTrafficOn") : t("payments.resetTrafficOff")}
            checked={form.resetTraffic}
            onChange={(v) => setForm((f) => ({ ...f, resetTraffic: v }))}
          />
          <SwitchRow
            label={t("payments.extraDevicesKeep")}
            sub={t("payments.extraDevicesKeepSub")}
            checked={form.keepDevices}
            onChange={(v) => setForm((f) => ({ ...f, keepDevices: v }))}
          />
          <div className="pt-4">
            <Field label={t("payments.trial")} htmlFor="pay-trial" hint={`${form.trialStars.trim() || form.trialRub.trim() ? t("payments.trialSubPaid") : t("payments.trialSub")}${s.trials ? ` ${t("payments.trialsGiven", { n: s.trials })}` : ""}`}>
              <Select id="pay-trial" className="input" value={form.trial} onChange={(e) => {
                  const trial = Number(e.target.value);
                  setForm((f) => (trial ? { ...f, trial } : { ...f, trial, trialStars: "", trialRub: "" }));
                }}>
                <option value={0}>{t("payments.trialOff")}</option>
                {(tariffs.data ?? []).map((tr) => (
                  <option key={tr.id} value={tr.id}>
                    {tr.name}
                  </option>
                ))}
              </Select>
            </Field>
            {form.trial ? (
              <div className="grid gap-x-3 sm:grid-cols-2" role="group" aria-label={t("payments.trialPrice")}>
                <Field label={`${t("payments.trialPrice")}, ⭐`} htmlFor="pay-trial-stars" error={errors.trial_stars}>
                  <input id="pay-trial-stars" className="input max-w-[140px]" inputMode="numeric" value={form.trialStars} onChange={(e) => setForm((f) => ({ ...f, trialStars: e.target.value }))} aria-invalid={!!errors.trial_stars} />
                </Field>
                <Field label={`${t("payments.trialPrice")}, ₽`} htmlFor="pay-trial-rub" error={errors.trial_rub}>
                  <input id="pay-trial-rub" className="input max-w-[140px]" inputMode="decimal" value={form.trialRub} onChange={(e) => setForm((f) => ({ ...f, trialRub: e.target.value }))} aria-invalid={!!errors.trial_rub} />
                </Field>
                <p className="text-xs text-[var(--ink-500)] sm:col-span-2">{t("payments.trialPriceSub")}</p>
              </div>
            ) : null}
          </div>
          <div className="pt-4">
            <Field label={t("payments.free")} htmlFor="pay-free" hint={`${t("payments.freeSub")}${s.free_plans ? ` ${t("payments.freeGiven", { n: s.free_plans })}` : ""}`}>
              <Select id="pay-free" className="input" value={form.free} onChange={(e) => setForm((f) => ({ ...f, free: Number(e.target.value) }))}>
                <option value={0}>{t("payments.freeOff")}</option>
                {(tariffs.data ?? []).map((tr) => (
                  <option key={tr.id} value={tr.id}>
                    {tr.name}
                  </option>
                ))}
              </Select>
            </Field>
            {form.free ? (
              <Field
                label={t("payments.freeCondition")}
                htmlFor="pay-free-condition"
                hint={form.freeCondition === "channel" ? t("payments.freeChannelHint") : !hasChannel ? t("payments.freeNoChannel") : undefined}
              >
                <Select id="pay-free-condition" className="input" value={form.freeCondition} onChange={(e) => setForm((f) => ({ ...f, freeCondition: e.target.value as Settings["free_condition"] }))}>
                  <option value="none">{t("payments.freeNone")}</option>
                  <option value="channel" disabled={!hasChannel && form.freeCondition !== "channel"}>
                    {t("payments.freeChannel")}
                  </option>
                </Select>
              </Field>
            ) : null}
          </div>
        </div>
        <FormActions dirty={dirty} saving={save.isPending} onReset={() => { reset(); setErrors({}); }} />
      </form>
    </section>
  );
}

// With selling off nothing takes payments: the readiness pill would only mislead, so it hides.
function Provider({ title, sub, on, onChange, live, selling, offline, error, children }: { title: string; sub: string; on: boolean; onChange: (v: boolean) => void; live: boolean; selling: boolean; offline?: string; error?: string; children?: ReactNode }) {
  return (
    <div className="mb-3" role="group" aria-label={title}>
      <div className="mb-3 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2 text-[13px] font-semibold">
            {title}
            {on && selling ? <Pill tone={live ? "ok" : "warn"}>{live ? t("payments.live") : t("payments.notReady")}</Pill> : null}
          </div>
          <div className="text-xs text-[var(--ink-500)]">{sub}</div>
          {offline ? <div className="mt-1 text-xs text-[var(--honey-600)]">{offline}</div> : null}
          {error ? (
            <div className="mt-1 text-xs text-[var(--berry-600)]" role="alert">
              {error}
            </div>
          ) : null}
        </div>
        <Switch checked={on} onChange={onChange} label={title} />
      </div>
      {on ? children : null}
    </div>
  );
}

const BALANCE_CURRENCIES = ["RUB", "USD", "EUR", "UAH", "BYN", "KZT", "UZS", "USDT"] as const;
type BalanceCurrency = (typeof BALANCE_CURRENCIES)[number];

/** Hundredths as the admin types them ("750", "750,50"); null when it is not a sum. */
function hundredths(text: string): number | null {
  const s = text.trim().replace(/\s/g, "").replace(",", ".");
  if (s === "") return 0;
  if (!/^\d{1,9}(\.\d{1,2})?$/.test(s)) return null;
  return Math.round(Number(s) * 100);
}

const units = (n: number) => (n ? String(n / 100).replace(".", ",") : "");

/** The balance's form as the settings hold it. */
const balanceForm = (b: Settings["balance"]) => ({
  enabled: b.enabled,
  currency: b.currency as BalanceCurrency,
  rate: String(b.rate || 1).replace(".", ","),
  details: b.details,
  min: units(b.min),
  max: units(b.max),
});

/**
 * The accounts' balance: topped up by a transfer and a screenshot the admin checks, then it
 * pays like any method at the tariffs' ruble prices, in its own currency at the admin's rate.
 */
function BalanceCard({ s }: { s: Settings }) {
  const save = useSavePayments();
  const { draft: form, setDraft: setForm, dirty, reset } = useDraft(balanceForm(s.balance));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const set = <K extends keyof typeof form>(k: K, v: (typeof form)[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
    setErrors(({ [k]: _, ...rest }) => rest);
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const next: Record<string, string> = {};
    const rate = Number(form.rate.replace(",", "."));
    const min = hundredths(form.min);
    const max = hundredths(form.max);
    if (form.currency !== "RUB" && !(rate > 0 && rate <= 1_000_000)) next.rate = t("payments.balance.rateErr");
    if (min === null) next.min = t("payments.balance.sumErr");
    if (max === null) next.max = t("payments.balance.sumErr");
    if (min !== null && max !== null && max > 0 && min > max) next.max = t("payments.balance.boundsErr");
    if (form.enabled && !form.details.trim()) next.details = t("payments.balance.detailsErr");
    setErrors(next);
    if (Object.keys(next).length) return;
    // The draft takes what the server keeps ("500.50" is 500,5 there), or the card stayed unsaved.
    save.mutate(
      { balance: { enabled: form.enabled, currency: form.currency, rate: form.currency === "RUB" ? 1 : rate, details: form.details.trim(), min: min ?? 0, max: max ?? 0 } },
      { onSuccess: (v) => setForm(balanceForm(v.balance)) },
    );
  };
  return (
    <section className="card glass reveal lg:col-span-2">
      <form onSubmit={submit} noValidate>
        {save.error ? <div className="banner err mb-4" role="alert">{errorText(save.error)}</div> : null}
        <Provider
          title={t("payments.balance.title")}
          sub={t("payments.balance.sub")}
          on={form.enabled}
          onChange={(v) => set("enabled", v)}
          live={s.available.balance}
          selling={s.enabled}
          offline={s.enabled && form.enabled && !s.available.balance ? t("payments.balance.notReady") : ""}
        >
          <div className="grid gap-x-4 md:grid-cols-2">
            <Field label={t("payments.balance.currency")} htmlFor="bal-currency">
              <Select id="bal-currency" className="input" value={form.currency} onChange={(e) => set("currency", e.target.value as BalanceCurrency)}>
                {BALANCE_CURRENCIES.map((c) => (
                  <option key={c} value={c}>
                    {t(`payments.balance.currencies.${c}`)}
                  </option>
                ))}
              </Select>
            </Field>
            {form.currency !== "RUB" ? (
              <Field label={t("payments.balance.rate", { currency: form.currency })} htmlFor="bal-rate" hint={t("payments.balance.rateHint")} error={errors.rate}>
                <label className="input-unit">
                  <input id="bal-rate" className="input" inputMode="decimal" value={form.rate} onChange={(e) => set("rate", e.target.value)} aria-invalid={!!errors.rate} />
                  <span>₽</span>
                </label>
              </Field>
            ) : null}
            <Field label={t("payments.balance.min")} htmlFor="bal-min" hint={t("payments.balance.boundHint")} error={errors.min}>
              <input id="bal-min" className="input" inputMode="decimal" value={form.min} onChange={(e) => set("min", e.target.value)} aria-invalid={!!errors.min} />
            </Field>
            <Field label={t("payments.balance.max")} htmlFor="bal-max" hint={t("payments.balance.boundHint")} error={errors.max}>
              <input id="bal-max" className="input" inputMode="decimal" value={form.max} onChange={(e) => set("max", e.target.value)} aria-invalid={!!errors.max} />
            </Field>
          </div>
          <Field label={t("payments.balance.details")} htmlFor="bal-details" hint={t("payments.balance.detailsHint")} error={errors.details}>
            <textarea id="bal-details" className="input min-h-[96px]" value={form.details} maxLength={1000} onChange={(e) => set("details", e.target.value)} placeholder={t("payments.balance.detailsPlaceholder")} aria-invalid={!!errors.details} />
          </Field>
        </Provider>
        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} />
      </form>
    </section>
  );
}
