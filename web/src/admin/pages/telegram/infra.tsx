// The admin's own chat for the panel's alerts, and the backups it gets there.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type Dispatch, type SetStateAction } from "react";
import { api, apiCodeText, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk } from "../../../api/hooks";
import { useSupportMode } from "../../../api/support";
import { CardHead } from "../../../components/layout";
import { Switch, SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, Field, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { onFormError } from "../../../lib/fields";
import { ago } from "../../../lib/format";
import { safeHref } from "../../../lib/url";
import { ClosedForSupport } from "../../support-closed";
import { usePatchTelegram, type View } from "./shared";
import { Select } from "../../../components/select";

export function InfrastructureCard({ v, draft, setDraft, error }: { v: View; draft: Schemas["AlertsConfig"]; setDraft: Dispatch<SetStateAction<Schemas["AlertsConfig"]>>; error: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [connectURL, setConnectURL] = useState("");
  const connect = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/telegram/infrastructure/connect", {})),
    onSuccess: (r) => {
      setConnectURL(r.url);
      toast.ok(t("telegram.infra.linkReady"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const disconnect = useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/v1/telegram/infrastructure/connect", {})),
    onSuccess: () => {
      setConnectURL("");
      void qc.invalidateQueries({ queryKey: qk.telegram });
      toast.ok(t("telegram.infra.disconnected"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const change = (key: keyof typeof draft, value: boolean | string) => setDraft((d) => ({ ...d, [key]: value }));
  const event = (key: keyof typeof draft.events, value: boolean) => setDraft((d) => ({ ...d, events: { ...d.events, [key]: value } }));
  return (
    <section className="card glass reveal">
      <CardHead title={t("telegram.infra.title")} sub={t("telegram.infra.subtitle")} />
      <div className="row-list">
        <SwitchRow label={t("telegram.infra.admin")} sub={t("telegram.infra.adminHint")} checked={draft.admin_enabled} onChange={(on) => change("admin_enabled", on)} />
        <div className="flex flex-wrap items-center justify-between gap-3 py-3">
          <div className="min-w-0">
            <div className="switch-row-title">{t("telegram.infra.adminChat")}</div>
            <div className="switch-row-sub">{v.admin_chat_set ? t("telegram.infra.adminConnected") : t("telegram.infra.adminNotConnected")}</div>
          </div>
          {v.admin_chat_set ? (
            <Button size="sm" variant="ghost" loading={disconnect.isPending} onClick={() => disconnect.mutate()}>
              {t("telegram.infra.disconnect")}
            </Button>
          ) : (
            <Button size="sm" loading={connect.isPending} disabled={!v.enabled} onClick={() => connect.mutate()}>
              {t("telegram.infra.connect")}
            </Button>
          )}
        </div>
        {safeHref(connectURL) ? (
          <div className="banner info my-3">
            <span>{t("telegram.infra.linkHint")}</span>
            <a className="font-medium underline" href={safeHref(connectURL, { tg: true })} target="_blank" rel="noreferrer noopener">
              {t("telegram.infra.openBot")}
            </a>
          </div>
        ) : null}
        <div className="grid gap-x-3 pt-4 sm:grid-cols-[1fr_160px]">
          <Field label={t("telegram.infra.group")} htmlFor="infra-group" hint={t("telegram.infra.groupHint")}>
            <input
              id="infra-group"
              className="input mono"
              value={draft.group_chat ?? ""}
              // A topic is of a group: without the group it goes too.
              onChange={(e) => setDraft((d) => ({ ...d, group_chat: e.target.value, ...(e.target.value.trim() ? {} : { group_thread: 0 }) }))}
              placeholder="-1001234567890"
              maxLength={40}
              autoComplete="off"
            />
          </Field>
          <Field label={t("telegram.infra.groupThread")} htmlFor="infra-thread" hint={t("telegram.infra.groupThreadHint")}>
            <input
              id="infra-thread"
              className="input mono"
              inputMode="numeric"
              value={draft.group_thread ? String(draft.group_thread) : ""}
              // 0, not left out: the PATCH keeps a field it is not sent.
              onChange={(e) => setDraft((d) => ({ ...d, group_thread: Number(e.target.value.replace(/\D/g, "").slice(0, 15)) }))}
              disabled={!draft.group_chat?.trim()}
              placeholder="42"
              autoComplete="off"
            />
          </Field>
        </div>
        <DiscordRow v={v} />
        <SwitchRow label={t("telegram.infra.public")} sub={t("telegram.infra.publicHint")} checked={draft.public_enabled} onChange={(on) => change("public_enabled", on)} />
        <div className="pt-4">
          <Field label={t("telegram.infra.channel")} htmlFor="infra-channel" hint={t("telegram.infra.channelHint")} error={error}>
            <input id="infra-channel" className="input mono" value={draft.public_channel ?? ""} onChange={(e) => change("public_channel", e.target.value)} placeholder="@my_status" maxLength={40} autoComplete="off" aria-invalid={!!error} />
          </Field>
        </div>
        <SwitchRow label={t("telegram.infra.summary")} sub={t("telegram.infra.summaryHint")} checked={draft.public_summary} onChange={(on) => change("public_summary", on)} />
        <SwitchRow label={t("telegram.infra.changes")} sub={t("telegram.infra.changesHint")} checked={draft.public_changes} onChange={(on) => change("public_changes", on)} />
      </div>
      <h3 className="mt-4 mb-1 text-[13px] font-semibold">{t("telegram.infra.events")}</h3>
      <p className="mb-3 text-xs text-[var(--ink-500)]">{t("telegram.infra.eventsHint")}</p>
      <div className="grid gap-2 sm:grid-cols-2">
        {(
          [
            ["node", "eventNode"],
            ["warp", "eventWarp"],
            ["exit", "eventExit"],
            ["inbound", "eventInbound"],
            ["autotune", "eventAutotune"],
            ["autotune_recovery", "eventAutotuneRecovery"],
            ["tls", "eventTLS"],
            ["update", "eventUpdate"],
            ["torrent", "eventTorrent"],
          ] as const
        ).map(([key, label]) => (
          <div key={key} className="panel-soft flex items-center justify-between gap-3 p-3">
            <span className="text-[13px] leading-5">{t(`telegram.infra.${label}`)}</span>
            <Switch checked={draft.events[key]} label={t(`telegram.infra.${label}`)} onChange={(on) => event(key, on)} />
          </div>
        ))}
      </div>
    </section>
  );
}

// The Discord webhook the admin's alerts go to as well. Its address is the secret of the
// channel: saved on its own (the server checks it with Discord), never shown back, only
// whether one is set and its name.
function DiscordRow({ v }: { v: View }) {
  const patch = usePatchTelegram();
  const toast = useToast();
  const [hook, setHook] = useState("");
  const [error, setError] = useState("");
  const save = (discord_webhook: string, done: string) =>
    patch.mutate(
      { infrastructure: { discord_webhook } },
      {
        onSuccess: () => {
          setHook("");
          setError("");
          toast.ok(done);
        },
        onError: (e) => onFormError(e, toast, (fields) => setError(Object.values(fields)[0] ?? "")),
      },
    );
  return (
    <div className="py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="switch-row-title">{t("telegram.infra.discord")}</div>
          <div className="switch-row-sub">
            {v.discord_set ? (v.discord_name ? t("telegram.infra.discordSet", { name: v.discord_name }) : t("telegram.infra.discordSetNoName")) : t("telegram.infra.discordNotSet")}
          </div>
        </div>
        {v.discord_set ? (
          <Button size="sm" variant="ghost" loading={patch.isPending && !hook} onClick={() => save("", t("telegram.infra.discordRemoved"))}>
            {t("telegram.infra.discordRemove")}
          </Button>
        ) : null}
      </div>
      <div className="pt-3">
        <Field label={t("telegram.infra.discordURL")} htmlFor="infra-discord" hint={v.discord_set ? t("telegram.infra.discordReplace") : t("telegram.infra.discordHint")} error={error}>
          <div className="flex gap-2">
            <input
              id="infra-discord"
              className="input mono"
              type="password"
              value={hook}
              onChange={(e) => setHook(e.target.value)}
              placeholder="https://discord.com/api/webhooks/…"
              maxLength={300}
              autoComplete="off"
              aria-invalid={!!error}
            />
            <Button type="button" loading={patch.isPending && !!hook} disabled={!hook.trim()} onClick={() => save(hook.trim(), t("telegram.infra.discordSaved"))}>
              {t("common.save")}
            </Button>
          </div>
        </Field>
      </div>
    </div>
  );
}

// The database to the admin chat, encrypted with the admin's password (tgbackup).
export function BackupCard() {
  const qc = useQueryClient();
  const toast = useToast();
  // The database copy goes to the admin's own chat: closed to a support operator.
  const operator = useSupportMode();
  // While a backup is being made in the background, its end is looked for every few seconds.
  const q = useQuery({
    queryKey: qk.telegramBackup,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/telegram/backup", { signal })),
    enabled: !operator,
    refetchInterval: (query) => (query.state.data?.sending ? 3000 : false),
  });
  const [password, setPassword] = useState("");
  const [shown, setShown] = useState(false);
  const [error, setError] = useState("");
  const done = (r: Schemas["BackupView"], text: string) => {
    qc.setQueryData(qk.telegramBackup, r);
    setPassword("");
    setShown(false);
    setError("");
    toast.ok(text);
  };
  // A phrase nobody will guess: 32 characters from the browser's random source, shown so
  // the admin can keep it before it is saved.
  const generate = () => {
    const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
    const bytes = crypto.getRandomValues(new Uint8Array(32));
    setPassword(Array.from(bytes, (x) => alphabet[x % alphabet.length]).join(""));
    setShown(true);
  };
  const fail = (e: unknown) => {
    onFormError(e, toast, (fields) => setError(Object.values(fields)[0] ?? ""));
  };
  const patch = useMutation({
    mutationFn: (body: { enabled?: boolean; hour?: number; password?: string }) => unwrap(api.PATCH("/api/v1/telegram/backup", { body })),
    onSuccess: (r) => done(r, t("telegram.infra.backupSaved")),
    onError: fail,
  });
  const send = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/telegram/backup/send", {})),
    onSuccess: (r) => done(r, t("telegram.infra.backupStarted")),
    onError: (e) => {
      fail(e);
      void qc.invalidateQueries({ queryKey: qk.telegramBackup });
    },
  });
  const b = q.data;
  if (operator) return <ClosedForSupport />;
  if (!b) return <Skeleton style={{ height: 220, borderRadius: 24 }} />;
  const size = b.last_size ? (b.last_size >= 1 << 20 ? `${(b.last_size / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(b.last_size / 1024))} KB`) : "";
  return (
    <section className="card glass">
      <CardHead title={t("telegram.infra.backup")} sub={t("telegram.infra.backupSub")} />
      {!b.admin_chat_set ? (
        <div className="banner warn mb-3">
          <span>{t("telegram.infra.backupNoChat")}</span>
        </div>
      ) : null}
      <div className="flex flex-wrap items-center justify-between gap-3 py-3">
        <div className="text-sm font-medium">{t("telegram.infra.backupOn")}</div>
        <Switch checked={b.enabled} label={t("telegram.infra.backupOn")} disabled={patch.isPending} onChange={(on) => patch.mutate(on && password ? { enabled: on, password } : { enabled: on })} />
      </div>
      <div className="grid gap-x-3 sm:grid-cols-[120px_1fr]">
        <Field label={t("telegram.infra.backupHour")} htmlFor="backup-hour">
          <Select id="backup-hour" className="input" value={b.hour} disabled={patch.isPending} onChange={(e) => patch.mutate({ hour: Number(e.target.value) })}>
            {Array.from({ length: 24 }, (_, h) => (
              <option key={h} value={h}>
                {String(h).padStart(2, "0")}:00
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("telegram.infra.backupPassword")} htmlFor="backup-password" hint={b.password_set ? t("telegram.infra.backupPasswordSet") : t("telegram.infra.backupPasswordHint")} error={error}>
          <div className="flex gap-2">
            <input id="backup-password" className="input mono" type={shown ? "text" : "password"} autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} maxLength={256} aria-invalid={!!error} />
            <Button type="button" variant="ghost" onClick={generate}>
              {t("telegram.infra.backupGenerate")}
            </Button>
            <Button type="button" loading={patch.isPending} disabled={password.length === 0} onClick={() => patch.mutate({ password })}>
              {t("common.save")}
            </Button>
          </div>
        </Field>
      </div>
      {shown ? <div className="banner warn mb-3">{t("telegram.infra.backupKeepIt")}</div> : null}
      <div className="mt-2 flex flex-wrap items-center justify-between gap-3">
        <div className="text-xs text-[var(--ink-500)]">
          {b.last_ok ? t("telegram.infra.backupLast", { when: ago(b.last_ok), size }) : t("telegram.infra.backupNever")}
          {b.last_error ? <div className="text-[var(--berry-600)]">{t("telegram.infra.backupFailed", { error: apiCodeText(b.last_error) })}</div> : null}
        </div>
        <Button size="sm" loading={send.isPending || b.sending} disabled={!b.admin_chat_set || !b.password_set || b.sending} onClick={() => send.mutate()}>
          {b.sending ? t("telegram.infra.backupSending") : t("telegram.infra.backupSendNow")}
        </Button>
      </div>
      <div className="mt-4 text-xs text-[var(--ink-500)]">{t("telegram.infra.backupRestore")}</div>
      <pre className="code-block mt-1 text-xs">{"age -d -o mikan.tar.gz mikan-….tar.gz.age\nmikan restore mikan.tar.gz"}</pre>
    </section>
  );
}
