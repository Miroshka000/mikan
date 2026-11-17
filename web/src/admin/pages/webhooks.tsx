// Outgoing webhooks (Settings): the addresses the panel sends its events to, each with the
// events it is told of and a secret that signs every request. Here the admin makes them, turns
// them on and off, sends a test event and reads the log of what was sent. The session alone
// reaches them: an address decides where the panel's events go, and may carry a token.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronLeft, ExternalLink, Plus, Send, Webhook as WebhookIcon } from "lucide-react";
import { useState } from "react";
import { api, errorText, unwrap } from "../../api/client";
import { useSupportMode } from "../../api/support";
import { CardHead } from "../../components/layout";
import { QueryBoundary } from "../../components/query";
import { Switch } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Button, EmptyState, PageHeader, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { ago } from "../../lib/format";
import { ClosedForSupport } from "../support-closed";
import { docsPage, failureText, shortAddress, stateOf, STATE_TONE, webhookKeys, type Webhook } from "./webhooks/common";
import { SecretDrawer, WebhookDrawer, type MadeSecret } from "./webhooks/form";
import { LogDrawer } from "./webhooks/log";

export function WebhooksPage() {
  // The addresses are the admin's own: closed to a support operator.
  const operator = !!useSupportMode();
  return (
    <>
      <Link to="/settings" search={{ tab: "security" }} className="mb-2 inline-flex items-center gap-1 rounded-lg text-[13px] font-medium text-[var(--ink-500)] hover:text-[var(--ink-900)]">
        <ChevronLeft size={16} aria-hidden /> {t("webhooks.back")}
      </Link>
      <PageHeader title={t("webhooks.title")} sub={t("webhooks.subtitle")} />
      {operator ? <ClosedForSupport /> : <AddressesCard />}
    </>
  );
}

function AddressesCard() {
  const list = useQuery({
    queryKey: webhookKeys.all,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/webhooks", { signal })),
    // What waits and how the last delivery went change on their own.
    refetchInterval: 15_000,
  });
  // The address a drawer was opened for is the one the list had then; the list refreshes under it.
  const [form, setForm] = useState<Webhook | "new" | null>(null);
  const [log, setLog] = useState<Webhook | null>(null);
  const [made, setMade] = useState<MadeSecret | null>(null);
  const full = !!list.data && list.data.items.length >= list.data.limit;
  return (
    <section className="card glass reveal" aria-labelledby="webhooks-title" aria-busy={list.isFetching && !list.isPending}>
      <CardHead
        id="webhooks-title"
        title={t("webhooks.cardTitle")}
        sub={t("webhooks.cardSub")}
        aside={
          <Button size="sm" variant="primary" disabled={!list.data || full} title={full ? t("webhooks.limit", { n: list.data?.limit ?? 0 }) : undefined} onClick={() => setForm("new")}>
            <Plus size={16} aria-hidden /> {t("webhooks.add")}
          </Button>
        }
      />
      <QueryBoundary
        query={list}
        pending={
          <div className="flex flex-col gap-2" role="status" aria-busy aria-label={t("common.loading")}>
            {[0, 1].map((i) => (
              <Skeleton key={i} style={{ height: 76 }} />
            ))}
          </div>
        }
      >
        {(data) =>
          data.items.length === 0 ? (
            <EmptyState icon={WebhookIcon} title={t("webhooks.emptyTitle")} text={t("webhooks.emptyText")}>
              <Button variant="primary" onClick={() => setForm("new")}>
                <Plus size={16} aria-hidden /> {t("webhooks.add")}
              </Button>
            </EmptyState>
          ) : (
            <>
              <ul className="row-list">
                {data.items.map((w) => (
                  <AddressRow key={w.id} w={w} onEdit={() => setForm(w)} onLog={() => setLog(w)} />
                ))}
              </ul>
              {full ? <p className="mt-3 text-xs text-[var(--ink-500)]">{t("webhooks.limit", { n: data.limit })}</p> : null}
            </>
          )
        }
      </QueryBoundary>
      <div className="form-actions">
        <a className="link-btn form-actions-note inline-flex items-center gap-1 text-[13px]" href={docsPage()} target="_blank" rel="noopener noreferrer">
          {t("webhooks.docs")} <ExternalLink size={14} aria-hidden />
        </a>
      </div>
      <WebhookDrawer target={form} onClose={() => setForm(null)} onSecret={setMade} />
      <LogDrawer target={log} onClose={() => setLog(null)} />
      <SecretDrawer made={made} onClose={() => setMade(null)} />
    </section>
  );
}

/** An address: how it fares, what it is told of, and what can be done with it. */
function AddressRow({ w, onEdit, onLog }: { w: Webhook; onEdit: () => void; onLog: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: webhookKeys.all });
  };
  const toggle = useMutation({
    mutationFn: (enabled: boolean) => unwrap(api.PATCH("/api/v1/webhooks/{id}", { params: { path: { id: w.id } }, body: { enabled } })),
    onSuccess: refresh,
    onError: (e) => toast.error(errorText(e)),
  });
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/webhooks/{id}/test", { params: { path: { id: w.id } } })),
    onSuccess: (r) => {
      if (r.ok) toast.ok(t("webhooks.testOk", { code: r.status, ms: r.duration_ms }));
      else toast.error(t("webhooks.testFailed", { reason: failureText(r.error, r.status) }));
      refresh();
      void qc.invalidateQueries({ queryKey: ["webhooks", "log", w.id] });
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const state = stateOf(w);
  const last = w.last;
  const meta = [
    w.events.length === 0 ? t("webhooks.allEvents") : t("webhooks.someEvents", { n: w.events.length }),
    w.waiting > 0 ? t("webhooks.waiting", { n: w.waiting }) : null,
    state === "ok" && last ? t("webhooks.lastOk", { ago: ago(last.updated_at) }) : null,
  ].filter(Boolean);
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 gap-y-3 py-4 max-[720px]:grid-cols-1">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="min-w-0 truncate text-[14px] font-semibold">{w.name}</h3>
          <Pill tone={STATE_TONE[state]}>{t(`webhooks.state.${state}`)}</Pill>
          {w.allow_local ? <span className="tag">{t("webhooks.local")}</span> : null}
        </div>
        <div className="mono mt-1 truncate text-xs text-[var(--ink-600)]">{shortAddress(w.url)}</div>
        <p className="mt-1 text-xs text-[var(--ink-500)]">{meta.join(" · ")}</p>
        {last && (state === "failing" || state === "failed") ? (
          <p className="mt-1 text-xs text-[var(--berry-600)]">{t("webhooks.lastTry", { n: last.attempts, reason: failureText(last.error, last.response_code) })}</p>
        ) : null}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Switch checked={w.enabled} label={t("webhooks.enable", { name: w.name })} disabled={toggle.isPending} onChange={(enabled) => toggle.mutate(enabled)} />
        <Button size="sm" loading={test.isPending} onClick={() => test.mutate()}>
          <Send size={16} aria-hidden /> {t("webhooks.test")}
        </Button>
        <Button size="sm" onClick={onLog}>
          {t("webhooks.log")}
        </Button>
        <Button size="sm" variant="ghost" onClick={onEdit}>
          {t("webhooks.edit")}
        </Button>
      </div>
    </li>
  );
}
