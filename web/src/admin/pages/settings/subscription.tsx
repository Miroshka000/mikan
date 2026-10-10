import { useState, type FormEvent } from "react";
import type { Schemas } from "../../../api/client";
import { useInbounds, useNodes } from "../../../api/hooks";
import { Disclosure, FormActions } from "../../../components/layout";
import { Button, Field, Pill } from "../../../components/ui";
import { SwitchRow } from "../../../components/switch";
import { t } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors } from "../../../lib/fields";
import { FingerprintSelect } from "../../../components/fingerprint-select";
import { useSaveSettings } from "./shared";
import { LimitPicker } from "../user/limit-picker";

// Ports the installer opens in the firewall (443 and the HTTPS pool): a subscription
// port among them needs nothing else on the server.
const OPEN_PORTS = [443, 2053, 2083, 2087, 2096, 8443] as const;

// Subscriptions on a port of their own: a usual HTTPS port looks like any site, and the
// admin panel's port stops showing in every link. Links on the old port keep working.
export function SubPortCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const inbounds = useInbounds();
  const nodes = useNodes();
  const { draft: port, setDraft: setPort } = useDraft(s.sub_port ? String(s.sub_port) : "");
  const error = fieldErrors(save.error).sub_port;
  const own = nodes.data?.find((n) => n.local)?.id;
  // Who holds a port over TCP on the panel's own server.
  const holder = (p: number) => (inbounds.data ?? []).find((i) => i.node_id === own && i.enabled && i.network === "tcp" && i.port === String(p))?.name;
  const value = Number(port);
  const valid = port.trim() === "" || (Number.isInteger(value) && value >= 1 && value <= 65535);
  const next = port.trim() === "" ? 0 : value;
  const changed = next !== (s.sub_port ?? 0);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (valid) save.mutate({ sub_port: next });
  };
  const current = s.sub_port || s.panel_port;
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.subPort")}</h2>
            <div className="card-sub">{t("settings.subPortSub")}</div>
          </div>
          {s.sub_port ? <Pill tone={s.sub_port_error ? "bad" : "ok"}>{s.sub_port_error ? t("settings.subPortDown") : t("settings.subPortOn", { port: s.sub_port })}</Pill> : null}
        </div>
        {s.sub_port_error ? (
          <div className="banner err mb-4" role="alert">
            {t("settings.subPortBusy", { port: s.sub_port, panel: s.panel_port })}
          </div>
        ) : null}
        {s.sub_base_url ? (
          <div className="panel-soft mb-4 p-3">
            <div className="text-xs text-[var(--ink-500)]">{t("settings.subPortLinks")}</div>
            <div className="mono truncate text-[13px]">{s.sub_base_url.replace(/[^/]+\/$/, "…")}</div>
          </div>
        ) : null}
        <Field label={t("settings.subPortField")} htmlFor="s-sub-port" hint={t("settings.subPortHint", { panel: s.panel_port })} error={error ?? (valid ? undefined : t("settings.subPortInvalid"))}>
          <input
            id="s-sub-port"
            className="input mono max-w-[140px]"
            inputMode="numeric"
            value={port}
            onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, ""))}
            placeholder={String(s.panel_port)}
            aria-invalid={!!error || !valid}
          />
        </Field>
        <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label={t("settings.subPortQuick")}>
          {OPEN_PORTS.map((p) => {
            const who = p === s.panel_port ? t("settings.subPortPanel") : holder(p);
            return (
              <button
                key={p}
                type="button"
                className="chip"
                aria-pressed={port === String(p)}
                disabled={!!who}
                title={p === s.panel_port ? t("errors.api.sub_port_panel") : who ? t("settings.subPortHeld", { name: who }) : undefined}
                onClick={() => setPort(String(p))}
              >
                <span className="mono">{p}</span>
                {who ? <span className="text-[var(--ink-400)]"> · {who}</span> : null}
              </button>
            );
          })}
        </div>
        <p className="text-xs text-[var(--ink-500)]">{t("settings.subPortNote")}</p>
        <FormActions saving={save.isPending && save.variables?.sub_port === next} disabled={!changed || !valid}>
          {s.sub_port ? (
            <Button variant="ghost" loading={save.isPending && save.variables?.sub_port === 0} onClick={() => save.mutate({ sub_port: 0 })}>
              {t("settings.subPortOff", { port: current === s.sub_port ? s.panel_port : current })}
            </Button>
          ) : null}
        </FormActions>
      </form>
    </section>
  );
}

export function SubscriptionCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const inbounds = useInbounds();
  const { draft: form, setDraft: setForm, dirty, reset } = useDraft({ sub_group_main: s.sub_group_main, sub_group_auto: s.sub_group_auto, client_fingerprint: s.client_fingerprint });
  const [fpOk, setFpOk] = useState(true);
  const errors = fieldErrors(save.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ sub_group_main: form.sub_group_main.trim(), sub_group_auto: form.sub_group_auto.trim(), client_fingerprint: form.client_fingerprint });
  };
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const proxies = (inbounds.data ?? []).filter((i) => i.enabled).map((i) => i.sub_name);
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.profile")}</h2>
            <div className="card-sub">{t("settings.subscriptionSub")}</div>
          </div>
        </div>
        <div className="grid gap-x-3 sm:grid-cols-2">
          <Field label={t("settings.groupMain")} htmlFor="s-gmain" hint={t("settings.groupMainHint")} error={errors.sub_group_main}>
            <input id="s-gmain" className="input" value={form.sub_group_main} onChange={set("sub_group_main")} maxLength={48} aria-invalid={!!errors.sub_group_main} autoComplete="off" />
          </Field>
          <Field label={t("settings.groupAuto")} htmlFor="s-gauto" hint={t("settings.groupAutoHint")} error={errors.sub_group_auto}>
            <input id="s-gauto" className="input" value={form.sub_group_auto} onChange={set("sub_group_auto")} maxLength={48} aria-invalid={!!errors.sub_group_auto} autoComplete="off" />
          </Field>
        </div>
        <div className="panel-soft mb-4 p-3" aria-label={t("settings.preview")}>
          <div className="mb-2 text-xs text-[var(--ink-500)]">{t("settings.previewHint")}</div>
          <b className="block truncate text-[13px]">{form.sub_group_main || "—"}</b>
          <div className="mt-2 flex flex-wrap gap-2">
            <span className="tag">{form.sub_group_auto || "—"}</span>
            {proxies.map((p) => (
              <span key={p} className="tag">
                {p}
              </span>
            ))}
          </div>
        </div>
        <Disclosure title={t("common.advanced")} open={!!errors.client_fingerprint || !fpOk}>
          <Field label={t("settings.fingerprint")} htmlFor="s-fp" hint={t("settings.fingerprintHint")} error={errors.client_fingerprint}>
            <FingerprintSelect key={s.client_fingerprint} id="s-fp" value={form.client_fingerprint} onChange={(v) => setForm((f) => ({ ...f, client_fingerprint: v }))} invalid={!!errors.client_fingerprint} onValid={setFpOk} />
          </Field>
        </Disclosure>
        <FormActions dirty={dirty} saving={save.isPending} onReset={reset} disabled={!dirty || !fpOk || !form.client_fingerprint} />
      </form>
    </section>
  );
}

// The variables the title and the announcement take (subs.TitleVars).
const TITLE_VARS = ["brand", "name", "date", "days", "used", "left", "total"] as const;

// The {words} of a text that are no variable: they stay as text in the apps, so this only
// warns (same case-sensitive match as the panel's substitution).
function unknownVars(text: string): string[] {
  const found = new Set<string>();
  for (const m of text.matchAll(/\{([A-Za-z0-9_-]+)\}/g)) {
    if (!(TITLE_VARS as readonly string[]).includes(m[1] ?? "")) found.add(m[0]);
  }
  return [...found];
}

// What the apps show besides the servers: an announcement for every app that reads one,
// and the brand for the apps that read operator headers (ClashFest, SlothClash).
export function AppsCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const { draft: form, setDraft: setForm, dirty, reset } = useDraft({ sub_title: s.sub_title, sub_announce: s.sub_announce, sub_announce_url: s.sub_announce_url, brand_accent: s.brand_accent, brand_logo_url: s.brand_logo_url });
  const errors = fieldErrors(save.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ sub_title: form.sub_title.trim(), sub_announce: form.sub_announce.trim(), sub_announce_url: form.sub_announce_url.trim(), brand_accent: form.brand_accent.trim(), brand_logo_url: form.brand_logo_url.trim() });
  };
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm((f) => ({ ...f, [k]: e.target.value }));
  // A variable chip goes into the field last typed in: the title until the announcement is.
  const [target, setTarget] = useState<"sub_title" | "sub_announce">("sub_title");
  const insert = (v: string) => setForm((f) => ({ ...f, [target]: (f[target] ? f[target].replace(/\s*$/, " ") : "") + `{${v}}` }));
  const unknownTitle = unknownVars(form.sub_title);
  const unknownAnnounce = unknownVars(form.sub_announce);
  const withUnknown = (hint: string, unknown: string[]) =>
    unknown.length === 0 ? (
      hint
    ) : (
      <>
        {hint} <span className="font-medium">{t("settings.unknownVars", { vars: unknown.join(" ") })}</span>
      </>
    );
  const accent = /^#[0-9A-Fa-f]{6}$/.test(form.brand_accent.trim()) ? form.brand_accent.trim() : "";
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.apps")}</h2>
            <div className="card-sub">{t("settings.appsSub")}</div>
          </div>
        </div>
        <Field label={t("settings.subTitle")} htmlFor="s-sub-title" hint={withUnknown(t("settings.subTitleHint"), unknownTitle)} error={errors.sub_title}>
          <input
            id="s-sub-title"
            className="input"
            value={form.sub_title}
            onChange={set("sub_title")}
            onFocus={() => setTarget("sub_title")}
            maxLength={200}
            placeholder={s.brand || "{brand}"}
            aria-invalid={!!errors.sub_title}
          />
        </Field>
        <Field label={t("settings.announce")} htmlFor="s-announce" hint={withUnknown(t("settings.announceHint"), unknownAnnounce)} error={errors.sub_announce}>
          <input
            id="s-announce"
            className="input"
            value={form.sub_announce}
            onChange={set("sub_announce")}
            onFocus={() => setTarget("sub_announce")}
            maxLength={200}
            aria-invalid={!!errors.sub_announce}
          />
        </Field>
        <div className="-mt-1 mb-3 flex flex-wrap items-center gap-2" role="group" aria-label={t("settings.titleVars")}>
          <span className="text-xs text-[var(--ink-500)]">{t("settings.titleVars")}</span>
          {TITLE_VARS.map((v) => (
            <button key={v} type="button" className="chip-btn" title={t(`settings.titleVar.${v}`)} onClick={() => insert(v)}>
              <span className="mono">{`{${v}}`}</span>
            </button>
          ))}
        </div>
        <Field label={t("settings.announceUrl")} htmlFor="s-announce-url" hint={t("settings.announceUrlHint")} error={errors.sub_announce_url}>
          <input id="s-announce-url" className="input" value={form.sub_announce_url} onChange={set("sub_announce_url")} placeholder="https://t.me/your_channel" aria-invalid={!!errors.sub_announce_url} />
        </Field>
        <SwitchRow className="border-t border-[var(--hairline)]" label={t("settings.appBranding")} sub={t("settings.appBrandingSub")} checked={s.app_branding} disabled={save.isPending} onChange={(v) => save.mutate({ app_branding: v })} />
        {s.app_branding ? (
          <div className="grid gap-x-3 sm:grid-cols-[160px_1fr]">
            <Field label={t("settings.brandAccent")} htmlFor="s-accent" hint={t("settings.brandAccentHint")} error={errors.brand_accent}>
              <div className="flex items-center gap-2">
                <span className="h-9 w-9 shrink-0 rounded-lg border border-[var(--hairline)]" style={{ background: accent || "transparent" }} aria-hidden />
                <input id="s-accent" className="input mono" value={form.brand_accent} onChange={set("brand_accent")} maxLength={7} placeholder="#F07A2E" autoComplete="off" aria-invalid={!!errors.brand_accent} />
              </div>
            </Field>
            <Field label={t("settings.brandLogo")} htmlFor="s-logo" hint={t("settings.brandLogoHint")} error={errors.brand_logo_url}>
              <input id="s-logo" className="input" value={form.brand_logo_url} onChange={set("brand_logo_url")} placeholder="https://example.com/logo-256.png" aria-invalid={!!errors.brand_logo_url} />
            </Field>
          </div>
        ) : null}
        <FormActions dirty={dirty} saving={save.isPending && save.variables?.app_branding === undefined} onReset={reset} />
      </form>
    </section>
  );
}

export function DevicesCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  return (
    <section className="card glass reveal" style={{ "--i": 4 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.devices")}</h2>
          <div className="card-sub">{t("settings.devicesSub")}</div>
        </div>
      </div>
      <ul className="row-list">
        <li>
          <SwitchRow label={t("settings.binding")} sub={t("settings.bindingSub")} checked={s.device_binding} disabled={save.isPending} onChange={(v) => save.mutate({ device_binding: v })} />
        </li>
        <li>
          <SwitchRow label={t("settings.requireHwid")} sub={t("settings.requireHwidSub")} checked={s.device_require_hwid} disabled={save.isPending || !s.device_binding} onChange={(v) => save.mutate({ device_require_hwid: v })} />
        </li>
      </ul>
      {s.device_binding ? <UnbindRules s={s} /> : null}
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.devicesNote")}</p>
    </section>
  );
}

/** A period of days in words: "a day", "a week", "30 days". */
function periodLabel(days: number): string {
  if (days === 1) return t("settings.unbindDay");
  if (days === 7) return t("settings.unbindWeek");
  if (days === 30) return t("settings.unbindMonth");
  return t("settings.unbindDays", { n: days });
}

/** A pause in hours: "at once", "6 h", whole days as days. */
function pauseLabel(hours: number): string {
  if (hours === 0) return t("settings.unbindAtOnce");
  if (hours % 24 === 0) return t("settings.unbindDays", { n: hours / 24 });
  return t("settings.unbindHours", { n: hours });
}

/**
 * How subscribers unbind devices themselves: how many a period, and how long an unbound
 * device stays out, so that its app, still open, does not take the place back at once.
 */
function UnbindRules({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const r = s.device_unbind;
  const set = (patch: Partial<Schemas["UnbindRules"]>, done: () => void) => save.mutate({ device_unbind: { ...r, ...patch } }, { onSuccess: done });
  const summary =
    r.limit === 0
      ? t("settings.unbindSummaryFree")
      : t("settings.unbindSummary", { n: r.limit, period: periodLabel(r.days).toLowerCase() });
  return (
    <div className="mt-4 border-t border-[var(--line)] pt-4">
      <h3 className="text-[14px] font-medium">{t("settings.unbindTitle")}</h3>
      <p className="mt-1 mb-3 text-xs text-[var(--ink-500)]">{t("settings.unbindSub")}</p>
      <div className="flex flex-col gap-3">
        <LimitPicker label={t("settings.unbindLimit")} value={r.limit === 0 ? null : r.limit} presets={[1, 2, 3, 5]} min={1} max={100} busy={save.isPending} onChange={(n, done) => set({ limit: n ?? 0 }, done)} />
        {r.limit > 0 ? (
          <LimitPicker
            label={t("settings.unbindPeriod")}
            value={r.days}
            presets={[1, 7, 30]}
            min={1}
            max={365}
            unlimited={false}
            format={periodLabel}
            fieldUnit={t("settings.unbindDaysUnit")}
            busy={save.isPending}
            onChange={(n, done) => set({ days: n ?? 1 }, done)}
          />
        ) : null}
        <LimitPicker
          label={t("settings.unbindReturn")}
          value={r.return_hours}
          presets={[0, 6, 24, 72]}
          min={0}
          max={720}
          unlimited={false}
          format={pauseLabel}
          fieldUnit={t("settings.unbindHoursUnit")}
          busy={save.isPending}
          onChange={(n, done) => set({ return_hours: n ?? 0 }, done)}
        />
      </div>
      <p className="mt-3 text-xs text-[var(--ink-600)]" role="status">
        {summary} {r.return_hours > 0 ? t("settings.unbindSummaryPause", { pause: pauseLabel(r.return_hours) }) : t("settings.unbindSummaryNoPause")}
      </p>
    </div>
  );
}
