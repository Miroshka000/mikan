import { keepPreviousData, useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Undo2 } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, usePaymentSettings } from "../../api/hooks";
import { Confirm } from "../../components/overlay";
import { useToast } from "../../components/toast";
import { QueryBoundary, StaleNotice } from "../../components/query";
import { Switch } from "../../components/switch";
import { Button, EmptyState, ErrorState, PageHeader, Pill, Skeleton, Spinner } from "../../components/ui";
import { t, tMaybe } from "../../i18n";
import { useDraft } from "../../lib/draft";
import { dateShort, days, money, num, time } from "../../lib/format";
import { AddonsCard, addonName, useAddons } from "./payment-addons";

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

/** Why a payment failed or waits, in words when the code is known; a provider's own code as is. */
function paymentError(code: string): string {
  return tMaybe(`errors.api.${code}`) ?? code;
}

/** A payment's provider as the admin knows it; adapters by their own name. */
function providerName(p: Provider, addons: Addons | undefined): string {
  return p.startsWith("addon:") ? addonName(p.slice("addon:".length), addons) : t(`payments.providers.${p}` as "payments.providers.stars");
}

export function PaymentsPage() {
  const settings = usePaymentSettings();
  const qc = useQueryClient();
  const toast = useToast();
  const sell = useMutation({
    mutationFn: () => unwrap(api.PATCH("/api/v1/payments/settings", { body: { enabled: true } })),
    onSuccess: (v) => {
      qc.setQueryData(qk.paymentSettings, v);
      toast.ok(t("settings.salesOnToast"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <>
      <PageHeader title={t("payments.title")} sub={t("payments.subtitle")} />
      {settings.data?.moving.length ? <MovingBanner ids={settings.data.moving} /> : null}
      {settings.data && !settings.data.enabled ? (
        <div className="banner warn mb-4 flex-wrap" role="status">
          <span className="min-w-0 flex-1">{t("payments.salesOff")}</span>
          <Button size="sm" variant="primary" loading={sell.isPending} onClick={() => sell.mutate()}>
            {t("payments.sellNow")}
          </Button>
        </div>
      ) : null}
      <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_420px]">
        <History />
        <div className="flex min-w-0 flex-col gap-4">
          <QueryBoundary query={settings} pending={<Skeleton style={{ height: 420, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
            {(s) => <SettingsCard s={s} />}
          </QueryBoundary>
          <AddonsCard selling={!!settings.data?.enabled} />
        </div>
      </div>
    </>
  );
}

/** The built-in YooKassa and CryptoBot moved to the marketplace: their adapters install after the update. */
function MovingBanner({ ids }: { ids: string[] }) {
  const addons = useAddons().data;
  const names = ids.map((id) => addonName(id, addons)).join(", ");
  return (
    <div className="banner info mb-4" role="status">
      <Spinner />
      <span className="min-w-0 flex-1">{t("payments.moving", { names })}</span>
    </div>
  );
}

function History() {
  const qc = useQueryClient();
  const toast = useToast();
  const [status, setStatus] = useState<Status | "">("");
  const [provider, setProvider] = useState<Provider | "">("");
  const [refund, setRefund] = useState<Payment | null>(null);
  const addons = useAddons().data;
  const providers = [...BUILT_IN, ...(addons?.installed.map((a) => `addon:${a.id}`) ?? [])];
  const list = useInfiniteQuery({
    queryKey: [...qk.payments, status, provider],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      unwrap(api.GET("/api/v1/payments", { params: { query: { status: status || undefined, provider: provider || undefined, before: pageParam || undefined, limit: 50 } } })),
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
  const items = list.data?.pages.flatMap((p) => p.items) ?? [];
  const totals = list.data?.pages[0]?.totals ?? [];
  return (
    <section className="card glass reveal min-w-0">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("payments.history")}</h2>
          <div className="card-sub">
            {totals.length ? totals.map((x) => t("payments.total", { n: num(x.count), sum: money(x.total, x.currency) })).join(" · ") : t("payments.noTotals")}
          </div>
        </div>
      </div>
      <div className="mb-4 flex flex-wrap gap-2">
        <select className="input max-w-[200px]" value={status} onChange={(e) => setStatus(e.target.value as Status | "")} aria-label={t("payments.status")}>
          <option value="">{t("payments.allStatuses")}</option>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {t(`payments.statuses.${s}`)}
            </option>
          ))}
        </select>
        <select className="input max-w-[200px]" value={provider} onChange={(e) => setProvider(e.target.value as Provider | "")} aria-label={t("payments.provider")}>
          <option value="">{t("payments.allProviders")}</option>
          {providers.map((p) => (
            <option key={p} value={p}>
              {providerName(p, addons)}
            </option>
          ))}
        </select>
      </div>
      {list.isError && list.data ? <StaleNotice onRetry={() => void list.refetch()} retrying={list.isFetching} /> : null}
      {list.isPending ? (
        <div className="flex flex-col gap-2">
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} style={{ height: 56 }} />
          ))}
        </div>
      ) : !list.data ? (
        <ErrorState text={errorText(list.error)} onRetry={() => void list.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState title={t("payments.empty")} text={status || provider ? t("payments.emptyFiltered") : t("payments.emptyText")} search={!!(status || provider)} />
      ) : (
        <>
          <ul className="row-list" aria-busy={list.isFetching}>
            {items.map((p) => (
              <PaymentRow key={p.id} p={p} provider={providerName(p.provider, addons)} onRefund={() => setRefund(p)} />
            ))}
          </ul>
          {list.hasNextPage ? (
            <Button className="mt-3" size="sm" loading={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
              {t("payments.more")}
            </Button>
          ) : null}
        </>
      )}
      <Confirm
        open={!!refund}
        onOpenChange={(v) => !v && setRefund(null)}
        title={t("payments.refundTitle", { sum: refund ? money(refund.amount, refund.currency) : "" })}
        text={t("payments.refundText")}
        confirm={t("payments.refund")}
        danger
        loading={doRefund.isPending}
        onConfirm={() => refund && doRefund.mutate(refund.id)}
      />
    </section>
  );
}

function PaymentRow({ p, provider, onRefund }: { p: Payment; provider: string; onRefund: () => void }) {
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
          {dateShort(p.created_at)} {time(p.created_at)} · {provider} · {p.kind === "new" ? t("payments.kindNew") : p.kind === "package" ? t("payments.kindPackage") : t("payments.kindRenew")} · {buyer}
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
          <div className="mt-1 text-xs text-[var(--berry-600)]">{t(p.status === "failed" ? "payments.invoiceFailed" : "payments.notApplied", { error: paymentError(p.error) })}</div>
        ) : null}
      </div>
      {p.provider === "stars" && p.status === "applied" ? (
        <Button size="sm" variant="ghost" onClick={onRefund}>
          <Undo2 size={16} aria-hidden /> {t("payments.refund")}
        </Button>
      ) : null}
    </li>
  );
}

function SettingsCard({ s }: { s: Settings }) {
  const qc = useQueryClient();
  const toast = useToast();
  const { draft: form, setDraft: setForm } = useDraft({ stars: s.stars, allowNew: s.allow_new, resetTraffic: s.renew_resets_traffic });
  const save = useMutation({
    mutationFn: (body: Schemas["PatchPaymentSettingsInputBody"]) => unwrap(api.PATCH("/api/v1/payments/settings", { body })),
    onSuccess: (v) => {
      qc.setQueryData(qk.paymentSettings, v);
      toast.ok(t("payments.saved"));
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ stars: form.stars, allow_new: form.allowNew, renew_resets_traffic: form.resetTraffic });
  };
  const set = (k: keyof typeof form) => (v: boolean) => setForm((f) => ({ ...f, [k]: v }));
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("payments.settings")}</h2>
            <div className="card-sub">{t("payments.settingsSub")}</div>
          </div>
        </div>
        {save.error ? <div className="banner err mb-4">{errorText(save.error)}</div> : null}
        {(s.available.stars || s.available.addons.length > 0) && s.on_sale === 0 ? (
          <div className="banner warn mb-4 flex-wrap" role="status">
            <span className="min-w-0 flex-1">{t("payments.nothingOnSale")}</span>
            <Link to="/tariffs" search={{ tab: "tariffs" }} className="btn btn-glass btn-sm">
              {t("payments.openTariffs")}
            </Link>
          </div>
        ) : null}

        <Provider title={t("payments.providers.stars")} sub={t("payments.starsSub")} on={form.stars} onChange={set("stars")} live={s.available.stars} selling={s.enabled} offline={s.enabled && form.stars && !s.available.stars ? t("payments.starsBotOff") : ""} />
        <p className="mb-4 text-xs text-[var(--ink-500)]">{t("payments.rublesInMarketplace")}</p>

        <div className="mb-4 flex items-start justify-between gap-3 border-t border-[var(--hairline)] pt-4">
          <div>
            <div className="text-[13px] font-semibold">{t("payments.allowNew")}</div>
            <div className="text-xs text-[var(--ink-500)]">{t("payments.allowNewSub")}</div>
          </div>
          <Switch checked={form.allowNew} onChange={set("allowNew")} label={t("payments.allowNew")} />
        </div>
        <div className="mb-4 flex items-start justify-between gap-3">
          <div>
            <div className="text-[13px] font-semibold">{t("payments.resetTraffic")}</div>
            <div className="text-xs text-[var(--ink-500)]">{form.resetTraffic ? t("payments.resetTrafficOn") : t("payments.resetTrafficOff")}</div>
          </div>
          <Switch checked={form.resetTraffic} onChange={set("resetTraffic")} label={t("payments.resetTraffic")} />
        </div>
        <Button type="submit" variant="primary" loading={save.isPending}>
          {t("common.save")}
        </Button>
      </form>
    </section>
  );
}

// With selling off nothing takes payments: the readiness pill would only mislead, so it hides.
function Provider({ title, sub, on, onChange, live, selling, offline, error, children }: { title: string; sub: string; on: boolean; onChange: (v: boolean) => void; live: boolean; selling: boolean; offline?: string; error?: string; children?: ReactNode }) {
  return (
    <div className="mb-4 border-t border-[var(--hairline)] pt-4 first-of-type:border-t-0 first-of-type:pt-0" role="group" aria-label={title}>
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
