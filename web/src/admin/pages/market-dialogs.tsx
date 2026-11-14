import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ExternalLink } from "lucide-react";
import { useEffect, useId, useState, type FormEvent } from "react";
import { ApiError, api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk } from "../../api/hooks";
import { Confirm, Drawer, DrawerFooter } from "../../components/overlay";
import { useToast } from "../../components/toast";
import { Button, Field, Pill, Segmented } from "../../components/ui";
import { t } from "../../i18n";
import { fieldErrors } from "../../lib/fields";
import { localized } from "../../lib/localized";
import { ShieldBanner } from "./market-parts";
import { safeHref } from "../../lib/url";
import { ADDON_ID_RE, PERMISSIONS, actsAsAdmin, installMarket, ownBotDocs, installPayment, sortPermissions, sourceLabel, type Kind, type MarketEntry, type MarketInstalled, type MarketOrigin } from "./market-common";

// The dialogs of the protocol 2 addons, shared by both tabs of the addons page: approving
// what an addon may do, its details, and an addon of the admin's own. Each one gets exactly
// the permissions the admin approved; an addon of a source, of the registry of community addons
// or of the admin's own (a process on the server) is code mikan has not seen, and says so
// everywhere: an install or an update of one waits for the admin to tick that they take the risk.

/**
 * The server's queue is one for both kinds of addons: a request of either shows in both. Who
 * holds the bot may change with it (a bot removed or installed).
 */
export function useRequested() {
  const qc = useQueryClient();
  const toast = useToast();
  return () => {
    void qc.invalidateQueries({ queryKey: qk.addons });
    void qc.invalidateQueries({ queryKey: qk.marketplace });
    void qc.invalidateQueries({ queryKey: qk.botSlot });
    toast.ok(t("addons.requested"));
  };
}

function shortDigest(digest = ""): string {
  return digest.length > 19 ? `${digest.slice(0, 19)}…` : digest;
}

export function permLabel(p: string): string {
  const k = PERMISSIONS.find((x) => x.id === p);
  return k ? t(k.label) : p;
}

/** The permissions an addon asks for, each with what it lets the addon do; new ones marked. */
function PermissionList({ perms, fresh = [] }: { perms: string[]; fresh?: string[] }) {
  if (!perms.length) return <p className="mt-4 text-[13px]">{t("market.approveNone")}</p>;
  return (
    <ul className="mt-4 flex flex-col gap-3">
      {sortPermissions(perms).map((p) => {
        const k = PERMISSIONS.find((x) => x.id === p);
        return (
          <li key={p} className="text-[13px]">
            <div className="flex flex-wrap items-center gap-2 font-semibold">
              {k ? t(k.label) : p}
              <span className="mono text-xs font-normal text-[var(--ink-500)]">{p}</span>
              {fresh.includes(p) ? <Pill tone="warn">{t("market.permNew")}</Pill> : null}
            </div>
            {k ? <div className="mt-1 text-xs text-[var(--ink-500)]">{t(k.text)}</div> : null}
          </li>
        );
      })}
    </ul>
  );
}

/** Where an unofficial addon comes from, and what that does and does not vouch for; a revoked one says why. */
function OriginWarning({ origin, className }: { origin: MarketOrigin; className?: string }) {
  if (origin.revoked) {
    return (
      <ShieldBanner tone="err" className={className}>
        {t("market.revokedText", { reason: origin.revoked })}
      </ShieldBanner>
    );
  }
  const community = origin.kind === "community";
  const key = community ? (origin.signed ? "market.communitySigned" : "market.communityUnsigned") : origin.signed ? "market.originSigned" : "market.originUnsigned";
  return (
    <ShieldBanner tone={community ? "err" : "warn"} className={className}>
      {t(key, { title: origin.title })}
    </ShieldBanner>
  );
}

/** A box the admin ticks before an unofficial addon goes in. */
function Tick({ checked, onChange, children }: { checked: boolean; onChange: (v: boolean) => void; children: string }) {
  return (
    <label className="mt-4 flex items-start gap-2 text-[13px] font-medium">
      <input type="checkbox" className="check mt-0.5 shrink-0" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span className="min-w-0 flex-1">{children}</span>
    </label>
  );
}

/** The image an unofficial addon runs, and for an update what it ran before; its author's repository. */
function BuildChange({ entry }: { entry: MarketEntry }) {
  const repo = safeHref(entry.origin?.repo ?? "");
  return (
    <dl className="mt-4 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-[13px]">
      {entry.installed_version ? (
        <>
          <dt className="text-[var(--ink-500)]">{t("addons.version")}</dt>
          <dd className="num">{`${entry.installed_version} → ${entry.version}`}</dd>
        </>
      ) : null}
      <dt className="text-[var(--ink-500)]">{t("market.image")}</dt>
      <dd className="mono break-all">
        {entry.installed_digest ? <span className="text-[var(--ink-500)]">{`${shortDigest(entry.installed_digest)} → `}</span> : null}
        {`${entry.image}@${entry.digest}`}
      </dd>
      {repo ? (
        <>
          <dt className="text-[var(--ink-500)]">{t("market.repo")}</dt>
          <dd className="break-all">
            <a className="link-btn" href={repo} target="_blank" rel="noopener noreferrer">
              {entry.origin?.title}
            </a>
          </dd>
        </>
      ) : null}
    </dl>
  );
}

function AdminWarning() {
  return (
    <ShieldBanner tone="err" className="mt-4">
      {t("market.adminWarn")}
    </ShieldBanner>
  );
}

/**
 * The approval: the admin sees exactly what the catalog's build asks for and sends that very
 * list back; a catalog that changed in between is refused by the server and shown again.
 */
export function ApproveDialog({ entry, granted, onClose }: { entry: MarketEntry | null; granted: string[]; onClose: () => void }) {
  const install = useInstallMarket({ onDone: onClose });
  const [risk, setRisk] = useState(false);
  const [keyTaken, setKeyTaken] = useState(false);
  // Each build is confirmed on its own: a box ticked for another one does not carry over.
  useEffect(() => {
    setRisk(false);
    setKeyTaken(false);
  }, [entry?.id, entry?.digest]);
  const name = entry ? localized(entry.name, entry.id) : "";
  const fresh = entry?.installed ? entry.permissions.filter((p) => !granted.includes(p)) : [];
  const other = !!entry?.origin;
  const keyChanged = !!entry?.key_changed;
  return (
    <Confirm
      open={!!entry}
      onOpenChange={(v) => !v && onClose()}
      title={t(entry?.installed ? "market.approveUpdateTitle" : "market.approveTitle", { name })}
      text={t(other ? "market.approveTextUnofficial" : "market.approveText")}
      confirm={t(entry?.installed ? "market.approveUpdate" : "market.approve")}
      danger={other}
      loading={install.isPending}
      confirmDisabled={other && (!risk || (keyChanged && !keyTaken))}
      onConfirm={() => entry && install.mutate({ entry, ack: other, acceptKey: keyChanged ? entry.fingerprint : undefined })}
    >
      {entry ? (
        <>
          <PermissionList perms={entry.permissions} fresh={fresh} />
          {entry.origin ? <OriginWarning origin={entry.origin} className="mt-4" /> : null}
          {entry.image ? <BuildChange entry={entry} /> : null}
          {keyChanged ? (
            <ShieldBanner tone="err" className="mt-4">
              {t("market.keyChangedText", { fingerprint: entry.fingerprint ?? "" })}
            </ShieldBanner>
          ) : null}
          {actsAsAdmin(entry.permissions) ? <AdminWarning /> : null}
          {other ? (
            <Tick checked={risk} onChange={setRisk}>
              {t(entry.installed ? "market.riskUpdate" : "market.riskInstall")}
            </Tick>
          ) : null}
          {keyChanged ? (
            <Tick checked={keyTaken} onChange={setKeyTaken}>
              {t("market.keyCompared", { fingerprint: entry.fingerprint ?? "" })}
            </Tick>
          ) : null}
        </>
      ) : null}
    </Confirm>
  );
}

/** The author's repository of an addon of the registry, as a link when it is one. */
function RepoLink({ origin }: { origin: MarketOrigin }) {
  const href = safeHref(origin.repo ?? "");
  if (!href) return <>{origin.title}</>;
  return (
    <a className="link-btn" href={href} target="_blank" rel="noopener noreferrer">
      {origin.title}
    </a>
  );
}

/** What an installed addon is: where it comes from, how it runs and what it was allowed. */
export function MarketDetails({ a, name, onClose }: { a: MarketInstalled | null; name: string; onClose: () => void }) {
  return (
    <Drawer open={!!a} onOpenChange={(v) => !v && onClose()} title={name} meta={a ? `${t(`market.kind.${a.kind}`)} · ${sourceLabel(a)}` : undefined}>
      {a ? (
        <div className="flex flex-col gap-4 pt-1 text-[13px]">
          {a.origin ? (
            <OriginWarning origin={a.origin} />
          ) : a.source !== "official" ? (
            <ShieldBanner>{t("market.unverifiedHint")}</ShieldBanner>
          ) : null}
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2">
            {a.version ? (
              <>
                <dt className="text-[var(--ink-500)]">{t("addons.version")}</dt>
                <dd className="num">{a.version}</dd>
              </>
            ) : null}
            <dt className="text-[var(--ink-500)]">{t("market.ownMode")}</dt>
            <dd>{t(`market.mode.${a.mode}`)}</dd>
            {a.origin?.kind === "community" ? (
              <>
                <dt className="text-[var(--ink-500)]">{t("market.repo")}</dt>
                <dd className="break-all">
                  <RepoLink origin={a.origin} />
                </dd>
              </>
            ) : a.origin ? (
              <>
                <dt className="text-[var(--ink-500)]">{t("market.sourceOf")}</dt>
                <dd className="break-words">{a.origin.title}</dd>
              </>
            ) : null}
            {a.source === "custom" ? (
              <>
                <dt className="text-[var(--ink-500)]">{t("market.image")}</dt>
                <dd className="mono break-all" title={a.digest}>{`${a.image}@${shortDigest(a.digest)}`}</dd>
              </>
            ) : a.source === "external" ? (
              <>
                <dt className="text-[var(--ink-500)]">{t("market.address")}</dt>
                <dd className={a.url ? "mono break-all" : undefined}>{a.url || t("market.noAddress")}</dd>
              </>
            ) : null}
          </dl>
          <div>
            <h3 className="font-semibold">{t("market.granted")}</h3>
            {a.granted.length ? <PermissionList perms={a.granted} /> : <p className="mt-2 text-[var(--ink-500)]">{t("market.noPerms")}</p>}
            {actsAsAdmin(a.granted) ? <AdminWarning /> : null}
          </div>
        </div>
      ) : null}
    </Drawer>
  );
}

type Own = {
  id: string;
  url: string;
  kind: Kind;
  mode: "http" | "client";
  permissions: string[];
  confirm: boolean;
};

const URL_RE = /^http:\/\/127\.0\.0\.1:([0-9]{4,5})\/?$/;

/** What is wrong before asking the server, as the server would say it. */
function check(o: Own): Record<string, string> {
  const out: Record<string, string> = {};
  if (!ADDON_ID_RE.test(o.id)) out.id = t("errors.api.addon_id_invalid");
  const m = URL_RE.exec(o.url);
  const port = m ? Number(m[1]) : 0;
  if (o.url ? !(port >= 1024 && port <= 65535 && String(port) === m?.[1]) : o.mode !== "client") out.url = t("errors.api.addon_url_invalid");
  return out;
}

function ownOf(a: MarketInstalled | null): Own {
  if (!a) return { id: "", url: "", kind: "bot", mode: "http", permissions: [], confirm: false };
  return { id: a.id, url: a.url ?? "", kind: a.kind, mode: a.mode, permissions: a.granted, confirm: false };
}

/**
 * The developer mode: a process of the admin's own on a loopback port, new or with other
 * permissions. No container, nothing signed vouches for it; images come from sources.
 */
export function OwnDrawer({ open, onOpenChange, update }: { open: boolean; onOpenChange: (v: boolean) => void; update: MarketInstalled | null }) {
  const qc = useQueryClient();
  const requested = useRequested();
  const [o, setO] = useState<Own>(() => ownOf(update));
  const [tried, setTried] = useState(false);
  const install = useMutation({
    mutationFn: (body: Schemas["MarketExternalInputBody"]) => unwrap(api.POST("/api/v1/marketplace/external", { body })),
    onSuccess: (v) => {
      qc.setQueryData(qk.marketplace, v);
      requested();
      onOpenChange(false);
    },
  });
  // Each opening starts from the addon it is for (or from nothing), not from the last attempt.
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setO(ownOf(update));
      setTried(false);
      install.reset();
    }
  }
  const local = check(o);
  const server = fieldErrors(install.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    if (Object.keys(local).length || !o.confirm || install.isPending) return;
    install.mutate({
      id: o.id,
      url: o.url ? o.url.replace(/\/$/, "") : undefined,
      kind: o.kind,
      mode: o.mode,
      permissions: o.permissions,
      confirm: o.confirm,
    });
  };
  return (
    <Drawer
      open={open}
      onOpenChange={onOpenChange}
      title={update ? t("market.ownUpdateTitle", { name: update.id }) : t("market.ownTitle")}
      meta={t("market.ownMeta")}
      footer={
        <DrawerFooter form="own-addon" onCancel={() => onOpenChange(false)} loading={install.isPending} disabled={!o.confirm}>
          {update ? t("market.ownUpdate") : t("addons.install")}
        </DrawerFooter>
      }
    >
      <OwnForm
        o={o}
        set={(p) => setO((cur) => ({ ...cur, ...p }))}
        update={!!update}
        errors={{ ...(tried ? local : {}), ...server }}
        general={install.error && !Object.keys(server).length ? errorText(install.error) : ""}
        onSubmit={submit}
      />
    </Drawer>
  );
}

function OwnForm({
  o,
  set,
  update,
  errors,
  general,
  onSubmit,
}: {
  o: Own;
  set: (p: Partial<Own>) => void;
  update: boolean;
  errors: Record<string, string>;
  general: string;
  onSubmit: (e: FormEvent) => void;
}) {
  const uid = useId();
  const toggle = (p: string) => set({ permissions: o.permissions.includes(p) ? o.permissions.filter((x) => x !== p) : [...o.permissions, p] });
  const trustNote = `${uid}-trust-note`;
  return (
    <form id="own-addon" onSubmit={onSubmit} noValidate className="flex flex-col gap-4">
      <ShieldBanner>{t("market.ownWarn")}</ShieldBanner>
      {general ? (
        <div className="banner err" role="alert">
          {general}
        </div>
      ) : null}
      <Field label={t("market.ownId")} htmlFor={`${uid}-id`} hint={t("market.ownIdHint")} error={errors.id}>
        <input
          id={`${uid}-id`}
          className="input mono"
          value={o.id}
          maxLength={32}
          readOnly={update}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={!!errors.id}
          onChange={(e) => set({ id: e.target.value.trim() })}
        />
      </Field>
      <Field label={t("market.ownUrl")} htmlFor={`${uid}-url`} hint={t("market.ownUrlHint")} error={errors.url}>
        <input
          id={`${uid}-url`}
          className="input mono"
          value={o.url}
          maxLength={64}
          placeholder="http://127.0.0.1:8080"
          autoComplete="off"
          spellCheck={false}
          aria-invalid={!!errors.url}
          onChange={(e) => set({ url: e.target.value.trim() })}
        />
      </Field>
      <Field label={t("market.ownKind")}>
        <Segmented
          label={t("market.ownKind")}
          value={o.kind}
          onChange={(kind) => set({ kind })}
          options={(["bot", "ui", "generic"] as const).map((k) => ({ value: k, label: t(`market.kind.${k}`) }))}
        />
      </Field>
      {o.kind === "bot" ? <OwnBotSteps fill={() => set({ mode: "client", url: "", permissions: o.permissions.includes("bot") ? o.permissions : ["bot", ...o.permissions] })} /> : null}
      <Field label={t("market.ownMode")} hint={t("market.ownModeHint")}>
        <Segmented
          label={t("market.ownMode")}
          value={o.mode}
          onChange={(mode) => set({ mode })}
          options={(["http", "client"] as const).map((m) => ({ value: m, label: t(`market.mode.${m}`) }))}
        />
      </Field>
      <Field label={t("market.ownPerms")} hint={t("market.ownPermsHint")} error={errors.permissions}>
        <div className="flex flex-col gap-3">
          {PERMISSIONS.map((p) => (
            <label key={p.id} className="flex items-start gap-2 text-[13px]">
              <input type="checkbox" className="check mt-0.5 shrink-0" checked={o.permissions.includes(p.id)} onChange={() => toggle(p.id)} />
              <span className="min-w-0">
                <span className="font-semibold">{t(p.label)}</span> <span className="mono text-xs text-[var(--ink-500)]">{p.id}</span>
                <span className="mt-0.5 block text-xs text-[var(--ink-500)]">{t(p.text)}</span>
              </span>
            </label>
          ))}
        </div>
      </Field>
      {actsAsAdmin(o.permissions) ? <AdminWarning /> : null}
      <div className="field">
        <label className="flex items-start gap-2 text-[13px] font-semibold">
          <input
            type="checkbox"
            className="check mt-0.5 shrink-0"
            checked={o.confirm}
            aria-invalid={!!errors.confirm}
            aria-describedby={trustNote}
            onChange={(e) => set({ confirm: e.target.checked })}
          />
          <span>{t("market.ownTrust")}</span>
        </label>
        {errors.confirm ? (
          <span className="err" role="alert" id={trustNote}>
            {errors.confirm}
          </span>
        ) : (
          <span className="hint" id={trustNote}>
            {t("market.ownTrustHint")}
          </span>
        )}
      </div>
    </form>
  );
}

/**
 * The way to a bot of the admin's own on a Telegram framework: what the panel needs, what the
 * process is started with and the handover, with the form filled for long polling in one press.
 * The addon's token stays in the server's state file: the panel never shows it.
 */
function OwnBotSteps({ fill }: { fill: () => void }) {
  return (
    <div className="banner info flex-col items-start" role="note">
      <span className="font-semibold">{t("market.ownBot.title")}</span>
      <ol className="flex list-decimal flex-col gap-1 pl-5">
        <li>{t("market.ownBot.step1")}</li>
        <li>{t("market.ownBot.step2")}</li>
        <li>
          {t("market.ownBot.step3")}
          <code className="mono mt-1 block whitespace-pre-wrap break-all text-xs">{"MIKAN_BOT_API_URL=http://<panel_api>\nMIKAN_BOT_TOKEN=0:<panel_token>"}</code>
        </li>
        <li>{t("market.ownBot.step4")}</li>
      </ol>
      <div className="flex flex-wrap items-center gap-3">
        <Button size="sm" onClick={fill}>
          {t("market.ownBot.fill")}
        </Button>
        <a className="link-btn inline-flex items-center gap-1" href={ownBotDocs()} target="_blank" rel="noopener noreferrer">
          {t("market.ownBot.docs")} <ExternalLink size={14} aria-hidden />
        </a>
      </div>
    </div>
  );
}

/** Install or update a payment method from the catalog: a request to the server. */
export function useInstallPayment() {
  const qc = useQueryClient();
  const toast = useToast();
  const requested = useRequested();
  return useMutation({
    mutationFn: installPayment,
    onSuccess: (v) => {
      qc.setQueryData(qk.addons, v);
      requested();
    },
    onError: (e) => toast.error(errorText(e)),
  });
}

/**
 * Install a build of an addon of the catalog with the permissions its entry asks for, sent as
 * approved: from ApproveDialog, or at once for a build that asks for nothing more than it was
 * allowed (one that asks for more goes through ApproveDialog, never here). onDone runs when the
 * request is settled for its caller: sent, or refused because the catalog changed in between.
 */
export function useInstallMarket({ onDone }: { onDone?: () => void } = {}) {
  const qc = useQueryClient();
  const toast = useToast();
  const requested = useRequested();
  return useMutation({
    mutationFn: installMarket,
    onSuccess: (v) => {
      qc.setQueryData(qk.marketplace, v);
      requested();
      onDone?.();
    },
    onError: (e) => {
      toast.error(errorText(e));
      void qc.invalidateQueries({ queryKey: qk.marketplace });
      if (e instanceof ApiError && e.detail === "permissions_changed") onDone?.();
    },
  });
}

/** Remove an addon of either kind: a request to the server. */
export function useRemoveAddon(kind: "payment" | "market", onDone: () => void) {
  const qc = useQueryClient();
  const toast = useToast();
  const requested = useRequested();
  return useMutation({
    mutationFn: async (id: string) => {
      if (kind === "payment") qc.setQueryData(qk.addons, await unwrap(api.POST("/api/v1/addons/{id}/remove", { params: { path: { id } } })));
      else qc.setQueryData(qk.marketplace, await unwrap(api.POST("/api/v1/marketplace/{id}/remove", { params: { path: { id } } })));
    },
    onSuccess: () => {
      requested();
      onDone();
    },
    onError: (e) => toast.error(errorText(e)),
  });
}
