import { useMutation, useQueryClient } from "@tanstack/react-query";
import clsx from "clsx";
import { ChevronDown, RefreshCw } from "lucide-react";
import { useId, useLayoutEffect, useRef, useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk, useUpdates } from "../../../api/hooks";
import { Confirm } from "../../../components/overlay";
import { QueryBoundary, StaleNotice } from "../../../components/query";
import { useToast } from "../../../components/toast";
import { Disclosure, FormActions } from "../../../components/layout";
import { Button, Field, Pill, Skeleton } from "../../../components/ui";
import { SwitchRow } from "../../../components/switch";
import { getLocale, LOCALES, t } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors } from "../../../lib/fields";
import { inlineMarkdown } from "../../../lib/inline-md";
import { ago, utcHourLabel } from "../../../lib/format";
import { useSaveSettings } from "./shared";

/** What the service is called, where it lives and where its people ask for help: the first things to fill in. */
export function ServiceCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  // Saving another card replaces `s`: what is typed here stays.
  const { draft: form, setDraft: setForm, dirty, reset } = useDraft({ brand: s.brand, public_host: s.public_host, domain: s.domain, support_url: s.support_url, quiet_hour_utc: s.quiet_hour_utc });
  const errors = fieldErrors(save.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ brand: form.brand, public_host: form.public_host, domain: form.domain, support_url: form.support_url, quiet_hour_utc: form.quiet_hour_utc });
  };
  const set = (k: "brand" | "public_host" | "domain" | "support_url") => (e: React.ChangeEvent<HTMLInputElement>) => setForm((f) => ({ ...f, [k]: e.target.value }));
  return (
    <section className="card glass reveal">
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.service")}</h2>
            <div className="card-sub">{t("settings.serviceSub")}</div>
          </div>
        </div>
        <Field label={t("settings.brand")} htmlFor="s-brand" hint={t("settings.brandHint")} error={errors.brand}>
          <input id="s-brand" className="input" value={form.brand} onChange={set("brand")} maxLength={40} aria-invalid={!!errors.brand} />
        </Field>
        <div className="grid gap-x-3 sm:grid-cols-2">
          <Field label={t("settings.host")} htmlFor="s-host" hint={t("settings.hostHint")} error={errors.public_host}>
            <input id="s-host" className="input mono" value={form.public_host} onChange={set("public_host")} aria-invalid={!!errors.public_host} />
          </Field>
          <Field label={t("settings.domain")} htmlFor="s-domain" hint={t("settings.domainHint")} error={errors.domain}>
            <input id="s-domain" className="input mono" value={form.domain} onChange={set("domain")} placeholder="vpn.example.com" aria-invalid={!!errors.domain} />
          </Field>
        </div>
        <Field label={t("settings.support")} htmlFor="s-support" hint={t("settings.supportHint")} error={errors.support_url}>
          <input id="s-support" className="input" value={form.support_url} onChange={set("support_url")} placeholder="https://t.me/your_support" aria-invalid={!!errors.support_url} />
        </Field>
        <Disclosure title={t("common.advanced")} open={!!errors.quiet_hour_utc}>
          <Field label={t("settings.quietHour")} htmlFor="s-quiet" hint={t("settings.quietHourHint")} error={errors.quiet_hour_utc}>
            <select id="s-quiet" className="input max-w-[240px]" value={form.quiet_hour_utc} onChange={(e) => setForm((f) => ({ ...f, quiet_hour_utc: Number(e.target.value) }))}>
              {Array.from({ length: 24 }, (_, h) => (
                <option key={h} value={h}>
                  {utcHourLabel(h)}
                </option>
              ))}
            </select>
          </Field>
        </Disclosure>
        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} />
      </form>
    </section>
  );
}

/** What visitors get until they pick a language; the header's switch is this browser's own. */
export function LanguageCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const options = [
    { id: "auto", label: t("settings.langAuto") },
    ...LOCALES.map((l) => ({ id: l.id, label: l.label, lang: l.id })),
  ] as const;
  const current = (save.isPending && save.variables.default_lang) || s.default_lang;
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.lang")}</h2>
          <div className="card-sub">{t("settings.langSub")}</div>
        </div>
      </div>
      <div className="grid gap-2 sm:grid-cols-3" role="radiogroup" aria-label={t("settings.lang")} aria-busy={save.isPending}>
        {options.map((o) => (
          <button
            key={o.id}
            type="button"
            role="radio"
            aria-checked={current === o.id}
            className="opt"
            lang={"lang" in o ? o.lang : undefined}
            disabled={save.isPending}
            onClick={() => o.id !== s.default_lang && save.mutate({ default_lang: o.id })}
          >
            <span className="font-semibold">{o.label}</span>
          </button>
        ))}
      </div>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.langNote")}</p>
    </section>
  );
}

/** Global switches of the automatic fixes; each connection can opt out in its settings. */
export function AutoCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const rows = [
    { key: "auto_port", title: t("settings.autoPort"), sub: t("settings.autoPortSub"), on: s.auto_port },
    { key: "auto_sni", title: t("settings.autoSni"), sub: t("settings.autoSniSub"), on: s.auto_sni },
  ] as const;
  // Switches with a cost the admin takes on: said before they go on, and kept in view while on.
  const risky = [
    { key: "any_port", on: s.any_port, text: "anyPort" },
    { key: "wide_sni_scan", on: s.wide_sni_scan, text: "wideScan" },
  ] as const;
  const [warn, setWarn] = useState<(typeof risky)[number] | null>(null);
  return (
    <section className="card glass reveal" style={{ "--i": 3 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.auto")}</h2>
          <div className="card-sub">{t("settings.autoSub")}</div>
        </div>
      </div>
      <ul className="row-list">
        {rows.map((r) => (
          <li key={r.key}>
            <SwitchRow label={r.title} sub={r.sub} checked={r.on} disabled={save.isPending} onChange={(v) => save.mutate({ [r.key]: v })} />
          </li>
        ))}
        {risky.map((r) => (
          <li key={r.key}>
            <SwitchRow
              label={t(`settings.${r.text}`)}
              sub={t(`settings.${r.text}Sub`)}
              checked={r.on}
              disabled={save.isPending}
              onChange={(v) => (v ? setWarn(r) : save.mutate({ [r.key]: false }))}
            />
            {r.on ? <p className="banner warn mb-3 text-xs">{t(`settings.${r.text}On`)}</p> : null}
          </li>
        ))}
      </ul>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.autoNote")}</p>
      <Confirm
        open={!!warn}
        onOpenChange={(v) => !v && setWarn(null)}
        title={warn ? t(`settings.${warn.text}ConfirmTitle`) : ""}
        text={warn ? t(`settings.${warn.text}ConfirmText`) : ""}
        confirm={warn ? t(`settings.${warn.text}Confirm`) : ""}
        loading={save.isPending}
        onConfirm={() => warn && save.mutate({ [warn.key]: true }, { onSuccess: () => setWarn(null) })}
      />
    </section>
  );
}

/**
 * A release's notes, folded to a few lines that fade out, with a button to unfold them: a
 * long list no longer pushes the update button and the switches off the screen.
 */
function NotesList({ lines }: { lines: string[] }) {
  const [open, setOpen] = useState(false);
  const [long, setLong] = useState(false);
  const ref = useRef<HTMLUListElement>(null);
  const id = useId();
  // Folded only when it is taller than the fold: a short list shows as it is. Measured
  // folded, never open (an open list fits itself), and again once it folds back; keyed by
  // the text, as the polled card hands in a new array on every render.
  const text = lines.join("\n");
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && !open) setLong(el.scrollHeight > el.clientHeight + 4);
  }, [text, open]);
  return (
    <>
      <ul ref={ref} id={id} className={clsx("notes", !open && "notes-folded", !open && long && "notes-fade")}>
        {lines.map((l, i) => (
          <li key={i}>{inlineMarkdown(l.replace(/^[-*]\s*/, ""))}</li>
        ))}
      </ul>
      {long || open ? (
        <button type="button" className="notes-toggle" aria-expanded={open} aria-controls={id} onClick={() => setOpen((o) => !o)}>
          {open ? t("settings.notesLess") : t("settings.notesMore", { n: lines.length })}
          <ChevronDown size={16} aria-hidden className={clsx("transition-transform", open && "rotate-180")} />
        </button>
      ) : null}
    </>
  );
}

/** The release the panel runs, the newest one and the host updater's last run. */
export function UpdatesCard() {
  const u = useUpdates();
  const qc = useQueryClient();
  const toast = useToast();
  const [confirm, setConfirm] = useState(false);
  const put = (d: Schemas["UpdatesView"]) => qc.setQueryData(qk.updates, d);
  const fail = (e: unknown) => toast.error(errorText(e));
  const check = useMutation({ mutationFn: () => unwrap(api.POST("/api/v1/updates/check")), onSuccess: put, onError: fail });
  const auto = useMutation({
    mutationFn: (v: boolean) => unwrap(api.PATCH("/api/v1/updates", { body: { auto: v } })),
    onSuccess: (d) => {
      put(d);
      toast.ok(t("settings.saved"));
    },
    onError: fail,
  });
  const channel = useMutation({
    mutationFn: (beta: boolean) => unwrap(api.PATCH("/api/v1/updates", { body: { channel: beta ? "beta" : "stable" } })),
    onSuccess: (d) => {
      put(d);
      toast.ok(t("settings.saved"));
    },
    onError: fail,
  });
  const follow = useMutation({
    mutationFn: (v: boolean) => unwrap(api.PATCH("/api/v1/updates", { body: { nodes_follow: v } })),
    onSuccess: (d) => {
      put(d);
      toast.ok(t("settings.saved"));
    },
    onError: fail,
  });
  const goals = useMutation({
    mutationFn: (v: boolean) => unwrap(api.PATCH("/api/v1/updates", { body: { show_goals: v } })),
    onSuccess: (d) => {
      put(d);
      toast.ok(t("settings.saved"));
    },
    onError: fail,
  });
  const request = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/updates/request")),
    onSuccess: (d) => {
      put(d);
      setConfirm(false);
    },
    onError: fail,
  });
  const v = u.data;
  // Without data yet: the skeleton, or the error with a retry in a card. (With data the card below
  // shows it, and a failed refresh adds the notice at its top.)
  if (!v) {
    return (
      <QueryBoundary query={u} pending={<Skeleton style={{ height: 180, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
        {() => null}
      </QueryBoundary>
    );
  }
  const notes = v.notes[getLocale()] || v.notes.en || "";
  const running = v.host?.state === "running";
  const waiting = v.requested_at > 0 || running;
  const stale = v.requested_at > 0 && !running && Date.now() / 1000 - v.requested_at > 120;
  const lastAt = v.host?.at ? ago(v.host.at) : "";
  return (
    <section id="updates" className="card glass reveal">
      {u.isError ? <StaleNotice onRetry={() => void u.refetch()} retrying={u.isFetching} /> : null}
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.updates")}</h2>
          <div className="card-sub">{t("settings.updatesSub")}</div>
        </div>
        <Button size="sm" loading={check.isPending} onClick={() => check.mutate()}>
          <RefreshCw size={16} aria-hidden /> {t("settings.updatesCheck")}
        </Button>
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className="font-semibold">mikan {v.current}</span>
        {v.available ? (
          <Pill tone="warn">{t("settings.updatesOut", { v: v.latest })}</Pill>
        ) : v.unreachable && v.newest ? (
          <Pill tone="warn">{t("settings.updatesOut", { v: v.newest })}</Pill>
        ) : v.latest ? (
          <Pill tone="ok">{t("settings.updatesLatest")}</Pill>
        ) : (
          <Pill tone="off">{v.error === "no_release" ? t("settings.updatesNoRelease") : v.checked_at ? t("settings.updatesUnknown") : t("settings.updatesNever")}</Pill>
        )}
        {v.checked_at > 0 ? <span className="text-xs text-[var(--ink-500)]">{t("settings.updatesChecked", { ago: ago(new Date(v.checked_at * 1000).toISOString()) })}</span> : null}
      </div>
      {v.error && v.error !== "no_release" ? <p className="mt-2 text-xs text-[var(--berry-600)]">{t("settings.updatesError", { e: v.error })}</p> : null}
      {v.unreachable && v.newest ? <p className="mt-2 text-xs text-[var(--honey-600)]">{t("settings.updatesUnreachable", { v: v.newest })}</p> : null}
      {v.available && v.newest && !v.unreachable ? (
        <p className="mt-2 text-xs text-[var(--ink-500)]">{t("settings.updatesHop", { v: v.latest, newest: v.newest })}</p>
      ) : null}
      {v.available && notes ? (
        <div className="panel-soft mt-4 p-3">
          <div className="mb-2 text-xs font-semibold text-[var(--ink-500)]">{t("settings.updatesChanges", { v: v.latest })}</div>
          <NotesList key={v.latest} lines={notes.split("\n").filter((l) => l.trim())} />
        </div>
      ) : null}
      {v.available || waiting ? (
        <div className="mt-4 flex flex-wrap items-center gap-3">
          <Button variant="primary" loading={request.isPending || (waiting && !stale)} disabled={waiting} onClick={() => setConfirm(true)}>
            {waiting ? t("settings.updatesWaiting") : t("settings.updatesNow", { v: v.latest })}
          </Button>
        </div>
      ) : null}
      {waiting && !stale ? <p className="mt-2 text-xs text-[var(--ink-500)]">{t("settings.updatesWaitingText")}</p> : null}
      {stale ? <p className="mt-2 text-xs text-[var(--honey-600)]">{t("settings.updatesStale")}</p> : null}
      {v.host && !waiting ? (
        <p className={clsx("mt-3 text-xs", v.host.state === "failed" ? "text-[var(--berry-600)]" : "text-[var(--ink-500)]")}>
          {v.host.state === "failed"
            ? t("settings.updatesLastFailed", { v: v.host.version || v.latest, from: v.host.from, e: v.host.error.split("\n")[0] ?? "" })
            : t("settings.updatesLastOk", { v: v.host.version, from: v.host.from, ago: lastAt })}
        </p>
      ) : null}
      <ul className="row-list mt-4 border-t border-[var(--hairline)]">
        <li>
          <SwitchRow label={t("settings.updatesAuto")} sub={t("settings.updatesAutoSub")} checked={v.auto} disabled={auto.isPending} onChange={(on) => auto.mutate(on)} />
        </li>
        <li>
          <SwitchRow label={t("settings.updatesNodes")} sub={t("settings.updatesNodesSub")} checked={v.nodes_follow} disabled={follow.isPending} onChange={(on) => follow.mutate(on)} />
        </li>
        <li>
          <SwitchRow label={t("settings.updatesBeta")} sub={t("settings.updatesBetaSub")} checked={v.channel === "beta"} disabled={channel.isPending} onChange={(on) => channel.mutate(on)} />
        </li>
        <li>
          <SwitchRow label={t("settings.updatesGoals")} sub={t("settings.updatesGoalsSub")} checked={v.show_goals} disabled={goals.isPending} onChange={(on) => goals.mutate(on)} />
        </li>
      </ul>
      <Confirm
        open={confirm}
        onOpenChange={setConfirm}
        title={t("settings.updatesConfirmTitle", { v: v.latest })}
        text={t("settings.updatesConfirmText")}
        confirm={t("settings.updatesConfirm")}
        loading={request.isPending}
        onConfirm={() => request.mutate()}
      />
    </section>
  );
}
