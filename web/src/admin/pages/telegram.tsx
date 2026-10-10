import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { Link, useBlocker, useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowDown, ArrowUp, Bell, Bot, Globe, LayoutList, Link2, Megaphone, Network, Plus, PlugZap, Send, Shield, Trash2, TriangleAlert } from "lucide-react";
import { useMemo, useState, type Dispatch, type FormEvent, type SetStateAction } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useNodes, useSettings } from "../../api/hooks";
import { useDraft } from "../../lib/draft";
import { TELEGRAM_TABS } from "../search";
import { BackToAddons } from "./addons";
import { ago, num } from "../../lib/format";
import { Confirm } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { SaveBar, SectionNav, WithPreview } from "../../components/layout";
import { useToast } from "../../components/toast";
import { Bar, Button, Field, PageHeader, Pill, Segmented, Skeleton } from "../../components/ui";
import { Switch, SwitchRow } from "../../components/switch";
import { t, tMaybe, useLocale } from "../../i18n";
import { MarkdownHint, TgHtml, TgTextPreview, useTgPreview } from "./telegram-text";

type View = Schemas["TelegramView"];
type Config = Schemas["Config"];
type MenuButton = Schemas["MenuButton"];
type TextKey = keyof Schemas["Texts"];

const TAB_ICONS = { connect: PlugZap, menu: LayoutList, notify: Bell, infra: Network, broadcast: Megaphone } as const;

function useTelegram() {
  return useQuery({
    queryKey: qk.telegram,
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/telegram", { signal })),
    // A broadcast in progress moves every second; otherwise little changes.
    refetchInterval: (q) => (q.state.data?.broadcast?.active ? 2_000 : 10_000),
  });
}

function usePatchTelegram() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["PatchTelegramInputBody"]) => unwrap(api.PATCH("/api/v1/telegram", { body })),
    onSuccess: (v) => qc.setQueryData(qk.telegram, v),
  });
}

/**
 * The bot in four sections: connecting it, its menu and texts, notifications and options,
 * broadcasts. Menu, texts and options are one draft saved together from the bar below,
 * whichever section they were changed in.
 */
export function TelegramPage() {
  const tg = useTelegram();
  return (
    <>
      <PageHeader title={t("nav.telegram")} sub={t("telegram.subtitle")} actions={<BackToAddons />} />
      <QueryBoundary
        query={tg}
        pending={
          <div className="flex flex-col gap-4" role="status" aria-busy aria-label={t("common.loading")}>
            <Skeleton style={{ height: 72, borderRadius: 20 }} />
            <Skeleton style={{ height: 420, borderRadius: 20 }} />
          </div>
        }
        wrap={(state) => <section className="card glass">{state}</section>}
      >
        {(v) => <TelegramBody v={v} />}
      </QueryBoundary>
    </>
  );
}

function TelegramBody({ v }: { v: View }) {
  const { tab } = useSearch({ from: "/_app/addons/telegram" });
  const navigate = useNavigate({ from: "/addons/telegram" });
  const go = (next: (typeof TELEGRAM_TABS)[number]) => void navigate({ search: { tab: next }, replace: true });
  const patch = usePatchTelegram();
  const toast = useToast();
  // The drafts follow the server's copy while untouched and keep the edits when the copy
  // changes under them (a poll, the bot saved from another session). Menu, texts and options
  // are one draft, the admin's alerts another; the bar below saves whatever was changed.
  const { draft, setDraft, dirty, reset } = useDraft(v.config);
  const infrastructure = useDraft(v.infrastructure);
  const [infraError, setInfraError] = useState("");
  const changed = dirty || infrastructure.dirty;
  const save = () =>
    patch.mutate(
      { ...(dirty ? { config: draft } : {}), ...(infrastructure.dirty ? { infrastructure: infrastructure.draft } : {}) },
      {
        onSuccess: (r) => {
          setDraft(r.config);
          infrastructure.setDraft(r.infrastructure);
          setInfraError("");
          toast.ok(t("telegram.saved"));
        },
        onError: (e) => {
          if (infrastructure.dirty && e instanceof ApiError && Object.keys(e.fields).length) {
            setInfraError(Object.values(e.fields)[0] ?? errorText(e));
            go("infra");
          } else toast.error(errorText(e));
        },
      },
    );
  const discard = () => {
    reset();
    infrastructure.reset();
    setInfraError("");
  };
  // Leaving the page drops the drafts: ask first. Switching the section stays on the page.
  const leave = useBlocker({ shouldBlockFn: ({ current, next }) => changed && current.pathname !== next.pathname, enableBeforeUnload: () => changed, withResolver: true });
  const preview = (bare: boolean) => <Preview draft={draft} v={v} bare={bare} />;

  return (
    <>
      <BotSummary v={v} onConnect={() => go("connect")} />
      <SectionNav
        label={t("telegram.sections")}
        sections={TELEGRAM_TABS.map((id) => ({ id, label: t(`telegram.tabs.${id}`), icon: TAB_ICONS[id] }))}
        value={tab}
        onChange={go}
        narrow={tab === "infra" || tab === "broadcast"}
      >
        {tab === "connect" ? (
          <WithPreview title={t("telegram.preview")} preview={preview}>
            <ConnectCard v={v} />
            <RouteCard v={v} />
          </WithPreview>
        ) : tab === "menu" ? (
          <WithPreview title={t("telegram.preview")} preview={preview}>
            <MenuCard draft={draft} setDraft={setDraft} />
            <TextsCard draft={draft} setDraft={setDraft} defaults={v.defaults} />
          </WithPreview>
        ) : tab === "notify" ? (
          <WithPreview title={t("telegram.preview")} preview={preview}>
            <OptionsCard draft={draft} setDraft={setDraft} v={v} />
          </WithPreview>
        ) : tab === "infra" ? (
          <>
            <InfrastructureCard v={v} draft={infrastructure.draft} setDraft={infrastructure.setDraft} error={infraError} />
            <BackupCard />
          </>
        ) : (
          <BroadcastCard v={v} />
        )}
        <SaveBar dirty={changed} saving={patch.isPending} onSave={save} onReset={discard} />
      </SectionNav>
      <Confirm
        open={leave.status === "blocked"}
        onOpenChange={(open) => !open && leave.reset?.()}
        title={t("telegram.leaveTitle")}
        text={t("telegram.leaveText")}
        confirm={t("telegram.leaveConfirm")}
        danger
        onConfirm={() => leave.proceed?.()}
      />
    </>
  );
}

/** The bot at a glance above every section: who it is, whether it runs, and its switch. */
function BotSummary({ v, onConnect }: { v: View; onConnect: () => void }) {
  const patch = usePatchTelegram();
  const toast = useToast();
  const st = statusOf(v);
  return (
    <section className="card glass reveal" aria-label={t("telegram.summary")}>
      <div className="flex flex-wrap items-center gap-3">
        <span className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-[var(--hover)] text-[var(--ink-700)]" aria-hidden>
          <Bot size={20} />
        </span>
        <div className="min-w-0 flex-1">
          {v.token_set && v.bot ? (
            <a className="font-semibold text-[var(--ink-900)] hover:underline" href={`https://t.me/${encodeURIComponent(v.bot.username)}`} target="_blank" rel="noreferrer noopener">
              @{v.bot.username}
            </a>
          ) : (
            <div className="font-semibold">{t("telegram.notConnected")}</div>
          )}
          <div className="truncate text-xs text-[var(--ink-500)]">
            {v.token_set ? (
              <>
                {t("telegram.stats", { linked: v.linked, accounts: v.accounts })}
                {" · "}
                {v.mini_app_url ? t("telegram.miniAppOn") : t("telegram.miniAppNoCert")}
              </>
            ) : (
              t("telegram.connectSub")
            )}
          </div>
        </div>
        {v.token_set ? (
          <div className="flex items-center gap-3">
            <Pill tone={st.tone}>{st.text}</Pill>
            <Switch checked={v.enabled} label={t("telegram.enabled")} disabled={patch.isPending} onChange={(on) => patch.mutate({ enabled: on }, { onError: (e) => toast.error(errorText(e)) })} />
          </div>
        ) : (
          <Button variant="primary" onClick={onConnect}>
            <Link2 size={16} aria-hidden /> {t("telegram.connectButton")}
          </Button>
        )}
      </div>
      {v.error && v.enabled ? (
        <p className="mt-3 text-[13px] text-[var(--berry-600)]" role="alert">
          {tMaybe(`telegram.err.${v.error}`) ?? v.error}
        </p>
      ) : null}
    </section>
  );
}

function InfrastructureCard({ v, draft, setDraft, error }: { v: View; draft: Schemas["AlertsConfig"]; setDraft: Dispatch<SetStateAction<Schemas["AlertsConfig"]>>; error: string }) {
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
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("telegram.infra.title")}</h2>
          <div className="card-sub">{t("telegram.infra.subtitle")}</div>
        </div>
      </div>
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
        {connectURL ? (
          <div className="banner info my-3">
            <span>{t("telegram.infra.linkHint")}</span>
            <a className="font-medium underline" href={connectURL} target="_blank" rel="noreferrer noopener">
              {t("telegram.infra.openBot")}
            </a>
          </div>
        ) : null}
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

// The database to the admin chat, encrypted with the admin's password (tgbackup).
function BackupCard() {
  const qc = useQueryClient();
  const toast = useToast();
  // While a backup is being made in the background, its end is looked for every few seconds.
  const q = useQuery({
    queryKey: ["telegram-backup"],
    queryFn: () => unwrap(api.GET("/api/v1/telegram/backup", {})),
    refetchInterval: (query) => (query.state.data?.sending ? 3000 : false),
  });
  const [password, setPassword] = useState("");
  const [shown, setShown] = useState(false);
  const [error, setError] = useState("");
  const done = (r: Schemas["BackupView"], text: string) => {
    qc.setQueryData(["telegram-backup"], r);
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
    if (e instanceof ApiError && Object.keys(e.fields).length) setError(Object.values(e.fields)[0] ?? "");
    else toast.error(errorText(e));
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
      void qc.invalidateQueries({ queryKey: ["telegram-backup"] });
    },
  });
  const b = q.data;
  if (!b) return <Skeleton style={{ height: 220, borderRadius: 24 }} />;
  const size = b.last_size ? (b.last_size >= 1 << 20 ? `${(b.last_size / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(b.last_size / 1024))} KB`) : "";
  return (
    <section className="card glass">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("telegram.infra.backup")}</h2>
          <div className="card-sub">{t("telegram.infra.backupSub")}</div>
        </div>
      </div>
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
          <select id="backup-hour" className="input" value={b.hour} disabled={patch.isPending} onChange={(e) => patch.mutate({ hour: Number(e.target.value) })}>
            {Array.from({ length: 24 }, (_, h) => (
              <option key={h} value={h}>
                {String(h).padStart(2, "0")}:00
              </option>
            ))}
          </select>
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
          {b.last_error ? <div className="text-[var(--berry-600)]">{t("telegram.infra.backupFailed", { error: tMaybe(`errors.api.${b.last_error}`) ?? b.last_error })}</div> : null}
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

function statusOf(v: View): { tone: "ok" | "warn" | "bad" | "off"; text: string } {
  if (!v.enabled) return { tone: "off", text: t("telegram.off") };
  if (v.running && !v.error) return { tone: "ok", text: t("telegram.running") };
  if (v.running) return { tone: "warn", text: t("telegram.reconnecting") };
  if (!v.error) return { tone: "off", text: t("telegram.starting") };
  return { tone: "bad", text: t("telegram.stopped") };
}

// Cards rise in turn, as on the other tabs.
const rise = (i: number) => ({ className: "card glass reveal", style: { "--i": i } as React.CSSProperties });
// Menu buttons slide to their new place when moved, shown or hidden.
const slide = { type: "spring", stiffness: 520, damping: 40 } as const;

function ConnectCard({ v }: { v: View }) {
  const patch = usePatchTelegram();
  const toast = useToast();
  const [token, setToken] = useState("");
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [error, setError] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError("");
    patch.mutate(
      { token: token.trim(), enabled: true },
      {
        onSuccess: (r) => {
          setToken("");
          setEditing(false);
          toast.ok(t("telegram.connected", { name: r.bot?.username ?? "" }));
        },
        onError: (err) => {
          if (err instanceof ApiError && Object.keys(err.fields).length) setError(Object.values(err.fields)[0] ?? "");
          else setError(errorText(err));
        },
      },
    );
  };
  const showForm = !v.token_set || editing;
  return (
    <section {...rise(0)}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("telegram.connect")}</h2>
          <div className="card-sub">{v.token_set && v.bot ? v.bot.name : t("telegram.connectSub")}</div>
        </div>
      </div>
      {showForm ? (
        <form onSubmit={submit} noValidate>
          {!v.token_set ? (
            <ol className="mb-4 flex list-decimal flex-col gap-1 pl-5 text-[13px] text-[var(--ink-600)]">
              <li>{t("telegram.step1")}</li>
              <li>{t("telegram.step2")}</li>
            </ol>
          ) : null}
          <Field label={t("telegram.token")} htmlFor="tg-token" hint={t("telegram.tokenHint")} error={error}>
            <input id="tg-token" className="input mono" type="password" autoComplete="off" spellCheck={false} value={token} onChange={(e) => setToken(e.target.value)} placeholder="123456789:AAH…" aria-invalid={!!error} />
          </Field>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" type="submit" loading={patch.isPending} disabled={!token.trim()}>
              <Link2 size={16} aria-hidden /> {t("telegram.connectButton")}
            </Button>
            {editing ? (
              <Button variant="ghost" onClick={() => setEditing(false)}>
                {t("common.cancel")}
              </Button>
            ) : null}
          </div>
        </form>
      ) : (
        <div className="mt-4 flex flex-wrap gap-2">
          <Button size="sm" onClick={() => setEditing(true)}>
            {t("telegram.changeToken")}
          </Button>
          <Button size="sm" variant="danger" onClick={() => setRemoving(true)}>
            <Trash2 size={16} aria-hidden /> {t("telegram.removeToken")}
          </Button>
        </div>
      )}
      <Confirm
        open={removing}
        onOpenChange={setRemoving}
        title={t("telegram.removeTitle")}
        text={t("telegram.removeText")}
        confirm={t("telegram.removeToken")}
        danger
        loading={patch.isPending}
        onConfirm={() => patch.mutate({ token: "" }, { onSuccess: () => setRemoving(false), onError: (e) => toast.error(errorText(e)) })}
      />
    </section>
  );
}

type RouteMode = Schemas["TelegramRoute"]["mode"];
const ROUTE_ICON = { direct: Globe, node: Network, proxy: Shield } as const;

// A node opens the tunnel to Telegram since 0.4.2; dev builds and nodes not heard from yet
// are given the benefit of the doubt (the check on saving tells).
function tunnels(version?: string): boolean {
  const m = /^(\d+)\.(\d+)\.(\d+)/.exec(version ?? "");
  if (!m) return true;
  const [a, b, c] = [Number(m[1]), Number(m[2]), Number(m[3])];
  return a > 0 || b > 4 || (b === 4 && c >= 2);
}

// How the bot reaches Telegram: straight, through a node of the panel or a proxy — for a
// server where Telegram is blocked.
function RouteCard({ v }: { v: View }) {
  const patch = usePatchTelegram();
  const toast = useToast();
  const nodes = useNodes();
  const saved = v.route;
  const {
    draft: { mode, nodeId },
    setDraft: setRoute,
  } = useDraft<{ mode: RouteMode; nodeId: number }>({ mode: saved.mode, nodeId: saved.node_id ?? 0 });
  const setMode = (m: RouteMode) => setRoute((d) => ({ ...d, mode: m }));
  const setNodeId = (n: number) => setRoute((d) => ({ ...d, nodeId: n }));
  const [proxy, setProxy] = useState("");
  const [error, setError] = useState("");
  const remote = (nodes.data ?? []).filter((n) => !n.local);
  const nodeName = (id?: number) => remote.find((n) => n.id === id)?.name ?? `#${id}`;
  const now =
    saved.mode === "node"
      ? t("telegram.routeNowNode", { name: nodeName(saved.node_id) })
      : saved.mode === "proxy"
        ? t("telegram.routeNowProxy", { proxy: saved.proxy ?? "" })
        : t("telegram.routeNowDirect");
  const changed = mode !== saved.mode || (mode === "node" && nodeId !== (saved.node_id ?? 0)) || (mode === "proxy" && proxy.trim() !== "");
  const ready = mode === "direct" || (mode === "node" && nodeId > 0) || (mode === "proxy" && (proxy.trim() !== "" || !!saved.proxy));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError("");
    const route: Schemas["PatchTelegramInputBody"]["route"] =
      mode === "node" ? { mode, node_id: nodeId } : mode === "proxy" ? { mode, ...(proxy.trim() ? { proxy: proxy.trim() } : {}) } : { mode };
    patch.mutate(
      { route },
      {
        onSuccess: (r) => {
          setProxy("");
          const how = r.route.mode === "node" ? nodeName(r.route.node_id) : r.route.mode === "proxy" ? (r.route.proxy ?? "") : t("telegram.routeDirectShort");
          toast.ok(t("telegram.routeSaved", { how }));
        },
        onError: (err) => setError(err instanceof ApiError && Object.keys(err.fields).length ? (Object.values(err.fields)[0] ?? "") : errorText(err)),
      },
    );
  };
  const Icon = ROUTE_ICON[saved.mode];
  return (
    <section {...rise(1)}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("telegram.routeTitle")}</h2>
          <div className="card-sub">{t("telegram.routeSub")}</div>
        </div>
      </div>
      <div className="panel-soft mb-4 flex items-center gap-3 p-3">
        <span className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-[var(--hover)] text-[var(--ink-700)]" aria-hidden>
          <Icon size={20} />
        </span>
        <div className="min-w-0">
          <div className="text-xs text-[var(--ink-500)]">{t("telegram.routeNow")}</div>
          <div className="truncate text-[13px] font-semibold">{now}</div>
        </div>
      </div>
      <form onSubmit={submit} noValidate>
        <div className="mb-4">
          <Segmented
            value={mode}
            onChange={(m) => {
              setMode(m);
              setError("");
            }}
            label={t("telegram.routeTitle")}
            options={[
              { value: "direct", label: t("telegram.routeDirect") },
              { value: "node", label: t("telegram.routeNode") },
              { value: "proxy", label: t("telegram.routeProxy") },
            ]}
          />
        </div>
        {mode === "direct" ? <p className="mb-4 text-xs text-[var(--ink-500)]">{t("telegram.routeDirectHint")}</p> : null}
        {mode === "node" ? (
          nodes.isPending ? (
            <Skeleton style={{ height: 40, borderRadius: 12, maxWidth: 320 }} />
          ) : remote.length === 0 ? (
            <div className="banner warn mb-4 flex-wrap" role="status">
              <span className="min-w-0 flex-1">{t("telegram.routeNoNodes")}</span>
              <Link to="/nodes" className="btn btn-glass btn-sm">
                {t("telegram.routeOpenNodes")}
              </Link>
            </div>
          ) : (
            <Field label={t("telegram.routeNodeLabel")} htmlFor="tg-route-node" hint={t("telegram.routeNodeHint")} error={error}>
              <select id="tg-route-node" className="input max-w-[320px]" value={nodeId} onChange={(e) => setNodeId(Number(e.target.value))} aria-invalid={!!error}>
                <option value={0} disabled>
                  {t("telegram.routeNodePick")}
                </option>
                {remote.map((n) => (
                  <option key={n.id} value={n.id} disabled={!tunnels(n.version)}>
                    {tunnels(n.version) ? n.name : t("telegram.routeNodeOld", { name: n.name })}
                  </option>
                ))}
              </select>
            </Field>
          )
        ) : null}
        {mode === "proxy" ? (
          <Field label={t("telegram.routeProxyLabel")} htmlFor="tg-route-proxy" hint={saved.proxy ? t("telegram.routeProxyKeep", { proxy: saved.proxy }) : t("telegram.routeProxyHint")} error={error}>
            <input
              id="tg-route-proxy"
              className="input mono"
              type="text"
              autoComplete="off"
              spellCheck={false}
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
              placeholder="socks5://user:pass@203.0.113.5:1080"
              maxLength={512}
              aria-invalid={!!error}
            />
          </Field>
        ) : null}
        {error && mode === "direct" ? (
          <p className="mb-3 text-xs text-[var(--berry-600)]" role="alert">
            {error}
          </p>
        ) : null}
        {mode === "node" && !nodes.isPending && remote.length === 0 ? null : (
          <div className="form-actions">
            <Button variant="primary" type="submit" loading={patch.isPending} disabled={!changed || !ready}>
              {mode === "direct" ? t("common.save") : t("telegram.routeSave")}
            </Button>
          </div>
        )}
      </form>
    </section>
  );
}

const ACTIONS = ["sub", "devices", "connect", "renew", "support", "app"] as const;

function actionLabel(a: string): string {
  return tMaybe(`telegram.action.${a}`) ?? a;
}

function MenuCard({ draft, setDraft }: { draft: Config; setDraft: (c: Config) => void }) {
  const sample = useSampleVars();
  const set = (i: number, patch: Partial<MenuButton>) => setDraft({ ...draft, buttons: draft.buttons.map((b, j) => (j === i ? { ...b, ...patch } : b)) });
  const move = (i: number, d: -1 | 1) => {
    const list = [...draft.buttons];
    [list[i], list[i + d]] = [list[i + d]!, list[i]!];
    setDraft({ ...draft, buttons: list });
  };
  const add = (action: "url" | "page") => {
    setDraft({
      ...draft,
      buttons: [...draft.buttons, { id: `c${Date.now().toString(36)}`, action, label: action === "url" ? t("telegram.newLink") : t("telegram.newPage"), on: true, row: false, url: action === "url" ? "https://" : undefined, text: action === "page" ? "" : undefined }],
    });
  };
  const custom = (b: MenuButton) => !ACTIONS.includes(b.action as (typeof ACTIONS)[number]);
  const reduce = useReducedMotion();
  return (
    <section {...rise(1)}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("telegram.menu")}</h2>
          <div className="card-sub">{t("telegram.menuSub")}</div>
        </div>
      </div>
      <ul className="flex flex-col gap-2">
        <AnimatePresence initial={false}>
          {draft.buttons.map((b, i) => (
            <motion.li
              key={b.id}
              className="panel-soft p-3"
              layout={!reduce}
              initial={reduce ? false : { opacity: 0, scale: 0.96 }}
              animate={{ opacity: 1, scale: 1 }}
              exit={reduce ? { opacity: 0 } : { opacity: 0, scale: 0.96 }}
              transition={slide}
            >
              <div className="flex items-center gap-2">
                <Switch checked={b.on} label={t("telegram.showButton", { name: b.label })} onChange={(on) => set(i, { on })} />
                <input className="input h-10 min-w-0 flex-1" value={b.label} maxLength={40} onChange={(e) => set(i, { label: e.target.value })} aria-label={t("telegram.buttonLabel")} />
                <button type="button" className="icon-btn" disabled={i === 0} onClick={() => move(i, -1)} aria-label={t("telegram.moveUp")}>
                  <ArrowUp size={16} />
                </button>
                <button type="button" className="icon-btn" disabled={i === draft.buttons.length - 1} onClick={() => move(i, 1)} aria-label={t("telegram.moveDown")}>
                  <ArrowDown size={16} />
                </button>
                {custom(b) ? (
                  <button type="button" className="icon-btn" onClick={() => setDraft({ ...draft, buttons: draft.buttons.filter((_, j) => j !== i) })} aria-label={t("telegram.deleteButton", { name: b.label })}>
                    <Trash2 size={16} />
                  </button>
                ) : null}
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-[var(--ink-500)]">
                <span>{actionLabel(b.action)}</span>
                {i > 0 ? (
                  <label className="flex items-center gap-1.5">
                    <input type="checkbox" className="check" checked={b.row} onChange={(e) => set(i, { row: e.target.checked })} /> {t("telegram.sameRow")}
                  </label>
                ) : null}
              </div>
              {b.action === "url" ? <input className="input mono mt-2" value={b.url ?? ""} onChange={(e) => set(i, { url: e.target.value })} placeholder="https://… / tg://…" aria-label={t("telegram.buttonUrl")} /> : null}
              {b.action === "page" ? (
                <>
                  <textarea className="input mt-2" value={b.text ?? ""} maxLength={3000} onChange={(e) => set(i, { text: e.target.value })} placeholder={t("telegram.pagePlaceholder")} aria-label={t("telegram.pageText")} />
                  <div className="mt-1">
                    <MarkdownHint />
                  </div>
                  <TgTextPreview text={b.text ?? ""} vars={sample} label={t("telegram.md.preview")} />
                </>
              ) : null}
            </motion.li>
          ))}
        </AnimatePresence>
      </ul>
      <div className="mt-3 flex flex-wrap gap-2">
        <button type="button" className="chip-btn" onClick={() => add("url")} disabled={draft.buttons.length >= 20}>
          <Plus size={14} className="mr-1 inline" aria-hidden />
          {t("telegram.addLink")}
        </button>
        <button type="button" className="chip-btn" onClick={() => add("page")} disabled={draft.buttons.length >= 20}>
          <Plus size={14} className="mr-1 inline" aria-hidden />
          {t("telegram.addPage")}
        </button>
      </div>
    </section>
  );
}

const TEXTS: { key: TextKey; label: string }[] = [
  { key: "welcome", label: "telegram.text.welcome" },
  { key: "main", label: "telegram.text.main" },
  { key: "renew", label: "telegram.text.renew" },
  { key: "expiring", label: "telegram.text.expiring" },
  { key: "expired", label: "telegram.text.expired" },
  { key: "traffic_90", label: "telegram.text.traffic_90" },
  { key: "traffic_end", label: "telegram.text.traffic_end" },
];

function TextsCard({ draft, setDraft, defaults }: { draft: Config; setDraft: (c: Config) => void; defaults: Schemas["Texts"] }) {
  // The text being edited shows under its field as the bot will send it.
  const [active, setActive] = useState<TextKey | null>(null);
  const sample = useSampleVars();
  return (
    <section {...rise(2)}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("telegram.texts")}</h2>
          <div className="card-sub">{t("telegram.textsSub")}</div>
        </div>
      </div>
      <Field label={t("telegram.lang")} hint={t("telegram.langHint")}>
        <Segmented
          label={t("telegram.lang")}
          value={draft.lang}
          onChange={(lang) => setDraft({ ...draft, lang })}
          options={[
            { value: "ru", label: "Русский" },
            { value: "en", label: "English" },
          ]}
        />
      </Field>
      {TEXTS.map(({ key, label }) => (
        <Field key={key} label={tMaybe(label) ?? key} htmlFor={`tg-${key}`}>
          <>
            <textarea
              id={`tg-${key}`}
              className="input"
              rows={key === "main" || key === "welcome" ? 5 : 2}
              maxLength={3000}
              value={draft.texts[key]}
              placeholder={defaults[key]}
              onFocus={() => setActive(key)}
              onChange={(e) => setDraft({ ...draft, texts: { ...draft.texts, [key]: e.target.value } })}
            />
            {active === key ? <TgTextPreview text={draft.texts[key]} vars={sample} label={t("telegram.md.preview")} /> : null}
          </>
        </Field>
      ))}
      <p className="text-xs text-[var(--ink-500)]">{t("telegram.variables")}</p>
      <div className="mt-2">
        <MarkdownHint />
      </div>
    </section>
  );
}

const NOTICES: (keyof Schemas["Notify"])[] = ["expire_3d", "expire_1d", "expired", "traffic_90", "traffic_100"];

function OptionsCard({ draft, setDraft, v }: { draft: Config; setDraft: (c: Config) => void; v: View }) {
  const row = (title: string, sub: string, on: boolean, change: (v: boolean) => void) => (
    <li key={title}>
      <SwitchRow label={title} sub={sub} checked={on} onChange={change} />
    </li>
  );
  return (
    <section {...rise(3)}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("telegram.options")}</h2>
          <div className="card-sub">{t("telegram.optionsSub")}</div>
        </div>
      </div>
      <ul className="row-list">
        {row(t("telegram.miniApp"), v.mini_app_url ? t("telegram.miniAppSub") : t("telegram.miniAppNoCert"), draft.mini_app, (on) => setDraft({ ...draft, mini_app: on }))}
        {row(t("telegram.promoButton"), draft.mini_app ? t("telegram.promoButtonSub") : t("telegram.promoButtonNeedsApp"), draft.promo_button, (on) => setDraft({ ...draft, promo_button: on }))}
        {row(t("telegram.cleanChat"), t("telegram.cleanChatSub"), draft.clean_chat, (on) => setDraft({ ...draft, clean_chat: on }))}
        {row(t("telegram.quietNight"), t("telegram.quietNightSub"), draft.quiet_night, (on) => setDraft({ ...draft, quiet_night: on }))}
        {NOTICES.map((k) => row(t(`telegram.notice.${k}`), t("telegram.noticeSub"), draft.notify[k], (on) => setDraft({ ...draft, notify: { ...draft.notify, [k]: on } })))}
      </ul>
    </section>
  );
}

function BroadcastCard({ v }: { v: View }) {
  const toast = useToast();
  const qc = useQueryClient();
  const [text, setText] = useState("");
  const [confirm, setConfirm] = useState(false);
  const send = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/telegram/broadcast", { body: { text } })),
    onSuccess: (r) => {
      toast.ok(t("telegram.broadcastSent", { n: r.queued }));
      setText("");
      setConfirm(false);
      void qc.invalidateQueries({ queryKey: qk.telegram });
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const busy = !!v.broadcast?.active;
  const ready = v.running && v.accounts > 0 && !busy;
  // A broadcast fills only {brand}.
  const sample = useSampleVars();
  const brandOnly = useMemo(() => ({ brand: sample.brand ?? "VPN" }), [sample.brand]);
  return (
    <section {...rise(4)}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("telegram.broadcast")}</h2>
          <div className="card-sub">{t("telegram.broadcastSub", { n: v.accounts })}</div>
        </div>
      </div>
      <textarea className="input" rows={4} maxLength={3500} value={text} onChange={(e) => setText(e.target.value)} placeholder={t("telegram.broadcastPlaceholder")} aria-label={t("telegram.broadcast")} disabled={!v.running} />
      <div className="mt-1">
        <MarkdownHint />
      </div>
      <TgTextPreview text={text} vars={brandOnly} label={t("telegram.md.preview")} />
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <Button variant="primary" disabled={!ready || !text.trim()} onClick={() => setConfirm(true)}>
          <Send size={16} aria-hidden /> {t("telegram.broadcastButton")}
        </Button>
        <span className="text-xs text-[var(--ink-500)]">{busy ? t("telegram.broadcasting") : !v.running ? t("telegram.broadcastOff") : t("telegram.broadcastHint")}</span>
      </div>
      {v.broadcast && <BroadcastProgress b={v.broadcast} />}
      <Confirm
        open={confirm}
        onOpenChange={setConfirm}
        title={t("telegram.broadcastTitle", { n: v.accounts })}
        text={t("telegram.broadcastText")}
        confirm={t("telegram.broadcastButton")}
        loading={send.isPending}
        onConfirm={() => send.mutate()}
      />
    </section>
  );
}

/** How far the last broadcast went; the bot sends up to 20 a second. */
function BroadcastProgress({ b }: { b: Schemas["TelegramBroadcast"] }) {
  const done = b.sent + b.failed;
  const sec = Math.ceil((b.total - done) / 20);
  const eta = sec < 60 ? t("telegram.broadcastEtaSec", { n: Math.max(1, sec) }) : t("telegram.broadcastEtaMin", { n: Math.ceil(sec / 60) });
  return (
    <div className="mt-4 rounded-2xl border border-[var(--hairline)] bg-[var(--glass-strong)] p-3" aria-live="polite">
      <div className="flex items-baseline justify-between gap-3 text-[13px]">
        <span className="font-medium">{b.active ? t("telegram.broadcastGoing") : t("telegram.broadcastLast", { when: ago(new Date(b.started * 1000).toISOString()) })}</span>
        <span className="tabular-nums text-[var(--ink-500)]">{t("telegram.broadcastCount", { done: num(b.sent), total: num(b.total) })}</span>
      </div>
      <div className="mt-2" role="progressbar" aria-label={t("telegram.broadcast")} aria-valuemin={0} aria-valuemax={b.total} aria-valuenow={done}>
        <Bar pct={b.total ? (done / b.total) * 100 : 100} />
      </div>
      {(b.active || b.failed > 0) && (
        <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs text-[var(--ink-500)]">
          {b.active && <span>{eta}</span>}
          {b.failed > 0 && <span>{t("telegram.broadcastFailed", { n: num(b.failed) })}</span>}
        </div>
      )}
    </div>
  );
}

/** Sample values of the texts' {variables}, for the previews. */
function useSampleVars(): Record<string, string> {
  const settings = useSettings();
  const brand = settings.data?.brand || "VPN";
  const locale = useLocale();
  return useMemo(
    () => ({
      brand,
      name: t("telegram.sample.name"),
      state: t("telegram.sample.state"),
      term: t("telegram.sample.term"),
      traffic: t("telegram.sample.traffic"),
      devices: t("telegram.sample.devices"),
      until: t("telegram.sample.until"),
      days: t("telegram.sample.days"),
      used: t("telegram.sample.used"),
      left: t("telegram.sample.left"),
      limit: t("telegram.sample.limit"),
      reset: t("telegram.sample.reset"),
    }),
    // The sample texts are translated: they change with the language.
    [brand, locale],
  );
}

/** The main menu as a subscriber sees it in Telegram, with sample data. */
function Preview({ draft, v, bare }: { draft: Config; v: View; bare?: boolean }) {
  const settings = useSettings();
  const support = !!settings.data?.support_url;
  const sample = useSampleVars();
  const source = draft.texts.main || v.defaults.main;
  const html = useTgPreview(source, sample);
  // Until the panel answers, the text as typed with the samples in it.
  const text = source.replace(/\{(\w+)\}/g, (m, k: string) => sample[k] ?? m);
  const reduce = useReducedMotion();
  const rows: MenuButton[][] = [];
  for (const b of draft.buttons) {
    if (!b.on || (b.action === "support" && !support) || (b.action === "app" && !(draft.mini_app && v.mini_app_url))) continue;
    const last = rows[rows.length - 1];
    if (b.row && last && last.length < 3) last.push(b);
    else rows.push([b]);
  }
  // The bot adds «Промокоды» under the buttons itself while the Mini App is on.
  if (draft.promo_button && draft.mini_app && v.mini_app_url) {
    rows.push([{ id: "promo", action: "app", label: t("telegram.promoLabel"), on: true, row: false }]);
  }
  return (
    <section {...(bare ? {} : rise(1))} aria-label={t("telegram.preview")}>
      {bare ? (
        <p className="mb-3 text-[13px] text-[var(--ink-500)]">{t("telegram.previewSub")}</p>
      ) : (
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("telegram.preview")}</h2>
            <div className="card-sub">{t("telegram.previewSub")}</div>
          </div>
        </div>
      )}
      <div className="tg-chat">
        <div className="tg-bubble">{html.data && !html.isError ? <TgHtml html={html.data.html} /> : text}</div>
        <motion.div className="tg-keyboard" layout={!reduce} transition={slide}>
          <AnimatePresence initial={false} mode="popLayout">
            {rows.map((r) => (
              <motion.div key={r[0]!.id} className="tg-row" layout={!reduce} transition={slide}>
                <AnimatePresence initial={false} mode="popLayout">
                  {r.map((b) => (
                    <motion.span
                      key={b.id}
                      className="tg-btn"
                      layout={!reduce}
                      initial={reduce ? false : { opacity: 0, scale: 0.85 }}
                      animate={{ opacity: 1, scale: 1 }}
                      exit={reduce ? { opacity: 0 } : { opacity: 0, scale: 0.85 }}
                      transition={slide}
                    >
                      {b.label}
                    </motion.span>
                  ))}
                </AnimatePresence>
              </motion.div>
            ))}
          </AnimatePresence>
        </motion.div>
      </div>
      {!v.mini_app_url && draft.buttons.some((b) => b.action === "app" && b.on) ? (
        <p className="mt-3 flex items-start gap-2 text-xs text-[var(--ink-500)]">
          <TriangleAlert size={14} className="mt-0.5 shrink-0 text-[var(--honey-600)]" aria-hidden /> {t("telegram.miniAppNoCert")}
        </p>
      ) : null}
    </section>
  );
}
