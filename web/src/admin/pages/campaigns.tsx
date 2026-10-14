import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { errorText } from "../../api/client";
import { qk, rawJson } from "../../api/hooks";
import { Confirm } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { Switch } from "../../components/switch";
import { FormActions } from "../../components/layout";
import { useToast } from "../../components/toast";
import { Button, EmptyState, ErrorState, Field, Pill, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useCopy } from "../../lib/copy";
import { useDraft } from "../../lib/draft";
import { dateShort, num } from "../../lib/format";

type Campaign = {
  id: number;
  tag: string;
  name: string;
  note: string;
  enabled: boolean;
  link?: string;
  visits: number;
  bought: number;
  renewed: number;
  amount: number;
  currency: string;
};

type Stats = { campaigns: number; visits: number; bought: number; renewed: number };

type List = { items: Campaign[]; total: number; stats: Stats };

type Visit = { chat_id: number; name?: string; tag: string; first_seen: string };

export function useCampaigns() {
  return useQuery({
    queryKey: qk.campaigns,
    queryFn: ({ signal }) => rawJson<List>("/api/v1/campaigns?limit=100", { signal }),
  });
}

export function Campaigns() {
  const list = useCampaigns();
  return (
    <QueryBoundary query={list} pending={<Skeleton style={{ height: 360, borderRadius: 20 }} />}>
      {(v) => <CampaignCards v={v} />}
    </QueryBoundary>
  );
}

function CampaignCards({ v }: { v: List }) {
  return (
    <div className="flex flex-col gap-4">
      <Header stats={v.stats} />
      <AddCard />
      {v.items.length === 0 ? (
        <section className="card glass reveal">
          <EmptyState title={t("campaigns.empty")} text={t("campaigns.emptyText")} />
        </section>
      ) : (
        <ul className="flex flex-col gap-3">
          {v.items.map((c) => (
            <CampaignRow key={c.id} c={c} />
          ))}
        </ul>
      )}
    </div>
  );
}

function Header({ stats }: { stats: Stats }) {
  const items: [string, string][] = [
    [t("campaigns.statCampaigns"), num(stats.campaigns)],
    [t("campaigns.statVisits"), num(stats.visits)],
    [t("campaigns.statBought"), num(stats.bought)],
    [t("campaigns.statRenewed"), num(stats.renewed)],
  ];
  return (
    <section className="card glass reveal">
      <div className="card-head">
        <div className="min-w-0">
          <h2 className="card-title">{t("campaigns.title")}</h2>
          <div className="card-sub">{t("campaigns.subtitle")}</div>
        </div>
        <dl className="flex flex-wrap gap-x-5 gap-y-1" aria-label={t("campaigns.stats")}>
          {items.map(([label, value]) => (
            <div key={label}>
              <dt className="text-[11px] text-[var(--ink-500)]">{label}</dt>
              <dd className="num text-[15px] font-semibold">{value}</dd>
            </div>
          ))}
        </dl>
      </div>
    </section>
  );
}

/** A tag the admin makes by hand: the link works as soon as the bot is on. */
function AddCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const create = useMutation({
    mutationFn: (body: { tag: string; name: string; note: string }) =>
      rawJson<Campaign>("/api/v1/campaigns", { method: "POST", body: JSON.stringify(body), headers: { "Content-Type": "application/json" } }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.campaigns });
      toast.ok(t("campaigns.created"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const { draft: f, setDraft: setF, dirty, reset } = useDraft({ tag: "", name: "", note: "" });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!f.tag.trim()) return;
    create.mutate({ tag: f.tag.trim(), name: f.name, note: f.note }, { onSuccess: () => reset() });
  };
  return (
    <section className="card glass reveal">
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div className="min-w-0">
            <h2 className="card-title">{t("campaigns.newTitle")}</h2>
          </div>
        </div>
        {create.error ? <div className="banner err mb-4">{errorText(create.error)}</div> : null}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <Field label={t("campaigns.tag")} hint={t("campaigns.tagHint")}>
            <input className="input mono" maxLength={48} value={f.tag} onChange={(e) => setF((c) => ({ ...c, tag: e.target.value }))} placeholder="instagram-oct" />
          </Field>
          <Field label={t("campaigns.name")} hint={t("campaigns.nameHint")}>
            <input className="input" maxLength={120} value={f.name} onChange={(e) => setF((c) => ({ ...c, name: e.target.value }))} />
          </Field>
          <Field label={t("campaigns.note")} hint={t("campaigns.noteHint")}>
            <input className="input" maxLength={500} value={f.note} onChange={(e) => setF((c) => ({ ...c, note: e.target.value }))} />
          </Field>
        </div>
        <FormActions dirty={dirty || !!f.tag} saving={create.isPending} onReset={reset} label={t("campaigns.add")} />
      </form>
    </section>
  );
}

function CampaignRow({ c }: { c: Campaign }) {
  const qc = useQueryClient();
  const toast = useToast();
  const copyText = useCopy();
  const [open, setOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const save = useMutation({
    mutationFn: (body: { name: string; note: string; enabled: boolean }) =>
      rawJson<Campaign>(`/api/v1/campaigns/${c.id}`, { method: "PUT", body: JSON.stringify(body), headers: { "Content-Type": "application/json" } }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.campaigns });
      toast.ok(t("campaigns.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const del = useMutation({
    mutationFn: () => rawJson<Record<string, never>>(`/api/v1/campaigns/${c.id}`, { method: "DELETE" }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.campaigns });
      setConfirmDelete(false);
      toast.ok(t("campaigns.deleted"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const { draft: f, setDraft: setF, dirty, reset } = useDraft({ name: c.name, note: c.note });
  const conversion = c.visits > 0 ? Math.round((c.bought / c.visits) * 100) : 0;
  const title = c.name || c.tag;
  return (
    <li className="card glass reveal">
      <div className="card-head items-start">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="card-title">{title}</h3>
            <code className="pill mono">{c.tag}</code>
            <Pill tone={c.enabled ? "ok" : "off"}>{c.enabled ? t("common.enable") : t("common.disable")}</Pill>
          </div>
          {c.note ? <div className="mt-1 text-xs text-[var(--ink-500)]">{c.note}</div> : null}
        </div>
        <div className="flex items-center gap-2">
          <Switch checked={c.enabled} label={t("campaigns.on")} disabled={save.isPending} onChange={(on) => save.mutate({ name: f.name, note: f.note, enabled: on })} />
          <Button size="sm" variant="ghost" onClick={() => setConfirmDelete(true)} aria-label={t("common.delete")}>
            <Trash2 size={16} aria-hidden />
          </Button>
        </div>
      </div>

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="text-xs text-[var(--ink-500)]">{t("campaigns.link")}</span>
        {c.link ? (
          <div className="link-field min-w-[240px] flex-1">
            <span className="mono" title={c.link}>
              {c.link}
            </span>
            <button type="button" className="icon-btn" onClick={() => void copyText(c.link!, t("common.linkCopied"))} aria-label={t("common.copyLink")}>
              <Copy size={16} />
            </button>
          </div>
        ) : (
          <span className="text-xs text-[var(--ink-500)]">{t("campaigns.linkOff")}</span>
        )}
      </div>

      <dl className="flex flex-wrap gap-x-6 gap-y-2" aria-label={t("campaigns.stats")}>
        <Stat label={t("campaigns.visits")} value={num(c.visits)} />
        <Stat label={t("campaigns.bought")} value={num(c.bought)} />
        <Stat label={t("campaigns.renewed")} value={num(c.renewed)} />
        <Stat label={t("campaigns.conversion")} value={`${conversion}%`} />
      </dl>

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button size="sm" variant="glass" onClick={() => setOpen((v) => !v)}>
          {open ? t("campaigns.showLess") : t("campaigns.openVisits")}
        </Button>
      </div>
      {open ? <Visits id={c.id} /> : null}

      <form
        className="mt-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate({ name: f.name, note: f.note, enabled: c.enabled });
        }}
        noValidate
      >
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Field label={t("campaigns.name")}>
            <input className="input" maxLength={120} value={f.name} onChange={(e) => setF((x) => ({ ...x, name: e.target.value }))} />
          </Field>
          <Field label={t("campaigns.note")}>
            <input className="input" maxLength={500} value={f.note} onChange={(e) => setF((x) => ({ ...x, note: e.target.value }))} />
          </Field>
        </div>
        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} note={t("campaigns.onSub")} />
      </form>

      <Confirm
        open={confirmDelete}
        onOpenChange={(v) => !v && setConfirmDelete(false)}
        title={t("campaigns.deleteTitle", { name: title })}
        text={t("campaigns.deleteText")}
        confirm={t("common.delete")}
        danger
        loading={del.isPending}
        onConfirm={() => del.mutate()}
      />
    </li>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-[11px] text-[var(--ink-500)]">{label}</dt>
      <dd className="num text-[15px] font-semibold">{value}</dd>
    </div>
  );
}

/** Who arrived through a campaign's link. Loaded only while the list is open. */
function Visits({ id }: { id: number }) {
  const visits = useQuery({
    queryKey: [...qk.campaigns, "visits", id],
    queryFn: ({ signal }) => rawJson<{ items: Visit[]; total: number }>(`/api/v1/campaigns/${id}/visits?limit=50`, { signal }),
  });
  if (visits.isError) return <ErrorState text={errorText(visits.error)} onRetry={() => void visits.refetch()} />;
  if (visits.isPending) return <Skeleton style={{ height: 90 }} className="mt-3" />;
  const items = visits.data?.items ?? [];
  if (items.length === 0) return <p className="mt-3 text-sm text-[var(--ink-500)]">{t("campaigns.visitsEmpty")}</p>;
  return (
    <ul className="row-list mt-3">
      {items.map((v) => (
        <li key={v.chat_id} className="flex items-center justify-between gap-3 py-2">
          <span className="truncate text-[13px]">{v.name || `tg ${v.chat_id}`}</span>
          <span className="text-xs text-[var(--ink-500)]">{dateShort(v.first_seen)}</span>
        </li>
      ))}
    </ul>
  );
}
