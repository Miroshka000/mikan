import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight, Copy, Fingerprint, KeyRound, LogOut, ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../../api/client";
import { meQuery, qk } from "../../../api/hooks";
import { CardHead, FormActions } from "../../../components/layout";
import { Confirm } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { useToast } from "../../../components/toast";
import { Button, Field, QR, Segmented, Skeleton } from "../../../components/ui";
import { caName, certLine, CertPill, CertStatusBlock } from "../../../components/acme";
import { t } from "../../../i18n";
import { useCopy } from "../../../lib/copy";
import { fieldErrors, onFormError } from "../../../lib/fields";
import { ago, dateShort } from "../../../lib/format";
import { createPasskey, passkeyCancelled, passkeysSupported } from "../../../lib/passkey";

export function AccessCard({ s }: { s: Schemas["SettingsView"] }) {
  const toast = useToast();
  const [confirm, setConfirm] = useState(false);
  const reset = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/settings/reset-admin-path")),
    onSuccess: (r) => {
      toast.ok(t("settings.newLinkIssued"));
      window.setTimeout(() => window.location.assign(r.admin_url), 5500);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const copyText = useCopy();
  const copy = () => copyText(s.admin_url, t("settings.adminLinkCopied"));
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <CardHead title={t("settings.adminLink")} sub={t("settings.adminLinkSub")} />
      <div className="link-field">
        <span className="mono">{s.admin_url || t("settings.noHost")}</span>
        <button type="button" className="icon-btn" onClick={copy} aria-label={t("settings.copyAdminLink")}>
          <Copy size={18} />
        </button>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <Button variant="danger" size="sm" onClick={() => setConfirm(true)}>
          <KeyRound size={16} aria-hidden /> {t("settings.newLink")}
        </Button>
        <span className="text-xs text-[var(--ink-500)]">{t("settings.newLinkHint")}</span>
      </div>
      <Confirm
        open={confirm}
        onOpenChange={setConfirm}
        title={t("settings.newLinkTitle")}
        text={t("settings.newLinkText")}
        confirm={t("settings.newLinkConfirm")}
        danger
        loading={reset.isPending || reset.isSuccess}
        onConfirm={() => reset.mutate()}
      />
    </section>
  );
}

type CA = Schemas["SettingsView"]["acme_ca"];

export function CertificateCard({ s }: { s: Schemas["SettingsView"] }) {
  const c = s.certificate;
  const [caFocus, setCaFocus] = useState(0);
  const custom = c.kind === "custom";
  const switching = c.kind === "acme" && !!c.ca && c.ca !== c.ca_wanted;
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties}>
      <CardHead title={t("settings.cert")} sub={certLine(c, t("settings.certSelf"))} aside={<CertPill c={c} pending={t("settings.certTemp")} />} />
      <CertStatusBlock
        c={c}
        field="certificate"
        renew={() => unwrap(api.POST("/api/v1/settings/certificate/renew"))}
        save={(cert, key) => unwrap(api.PUT("/api/v1/settings/certificate", { body: { cert, key } }))}
        clear={() => unwrap(api.DELETE("/api/v1/settings/certificate"))}
        drawer={{ title: t("cert.panelTitle"), lead: t("cert.panelLead"), clearLabel: t("cert.panelClear"), clearText: t("cert.panelClearText") }}
        note={custom ? t("settings.certOwnNote") : t("settings.certNote")}
        switching={switching ? t("certs.switching", { ca: caName(c.ca_wanted) }) : undefined}
        // Without the panel's public address there is nothing to order a certificate for.
        canRenew={c.error !== "no_public_host"}
        onUseZeroSSL={() => setCaFocus((n) => n + 1)}
      />
      <AuthorityForm s={s} focus={caFocus} />
    </section>
  );
}

/**
 * Who issues the panel's and the nodes' certificates. ZeroSSL takes an e-mail (its API gives
 * the account's key for it), Google an EAB key made in Google Cloud. `focus` moves when the
 * check's "Switch to ZeroSSL" is pressed: ZeroSSL is picked and the e-mail field focused.
 */
function AuthorityForm({ s, focus }: { s: Schemas["SettingsView"]; focus: number }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [ca, setCa] = useState<CA>(s.acme_ca);
  const [email, setEmail] = useState(s.acme_email);
  const [kid, setKid] = useState(s.acme_eab_kid);
  const [hmac, setHmac] = useState("");
  const emailRef = useRef<HTMLInputElement>(null);
  const [seen, setSeen] = useState(focus);
  if (focus !== seen) {
    setSeen(focus);
    setCa("zerossl");
  }
  useEffect(() => {
    if (focus > 0) emailRef.current?.focus();
  }, [focus]);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH("/api/v1/settings", {
          body: { acme_ca: ca, acme_email: email.trim(), ...(ca === "google" ? { acme_eab_kid: kid.trim(), ...(hmac.trim() ? { acme_eab_hmac: hmac.trim() } : {}) } : {}) },
        }),
      ),
    onSuccess: (v) => {
      qc.setQueryData(qk.settings, v);
      setHmac("");
      toast.ok(t("certs.saved"));
      // The panel's order runs in the background: the card follows it.
      window.setTimeout(() => void qc.invalidateQueries({ queryKey: qk.settings }), 15_000);
    },
    onError: (e) => {
      onFormError(e, toast);
    },
  });
  const errors = fieldErrors(save.error);
  const dirty = ca !== s.acme_ca || email.trim() !== s.acme_email || (ca === "google" && (kid.trim() !== s.acme_eab_kid || hmac.trim() !== ""));
  const host = s.domain || s.public_host;
  const ipOnly = !s.domain && !!s.public_host;
  return (
    <form
      className="mt-5 border-t border-[var(--hairline)] pt-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (dirty) save.mutate();
      }}
      noValidate
    >
      <Field label={t("certs.ca")} hint={t("certs.caHint")}>
        <Segmented<CA>
          value={ca}
          onChange={setCa}
          label={t("certs.ca")}
          options={[
            { value: "letsencrypt", label: t("certs.caLetsencrypt") },
            { value: "zerossl", label: t("certs.caZerossl") },
            { value: "google", label: t("certs.caGoogle") },
          ]}
        />
      </Field>
      {ipOnly ? (
        <p className="mb-4 text-xs text-[var(--ink-500)]">{t("certs.ipOnlyLe")}</p>
      ) : ca === "letsencrypt" && host ? (
        <p className="mb-4 text-xs text-[var(--ink-500)]">{t("certs.oldAndroid")}</p>
      ) : null}
      <Field label={t("certs.email")} htmlFor="acme-email" hint={ca === "zerossl" ? t("certs.emailHint") : t("certs.emailOptional")} error={errors.acme_email}>
        <input id="acme-email" ref={emailRef} type="email" className="input max-w-[360px]" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} required={ca === "zerossl"} />
      </Field>
      {ca === "google" ? (
        <>
          <Field label={t("certs.eabKid")} htmlFor="acme-kid" error={errors.acme_eab_kid}>
            <input id="acme-kid" className="input mono max-w-[360px]" autoComplete="off" spellCheck={false} value={kid} onChange={(e) => setKid(e.target.value)} />
          </Field>
          <Field label={t("certs.eabHmac")} htmlFor="acme-hmac" hint={s.acme_eab_hmac_set ? t("certs.eabSaved") : t("certs.eabHint")} error={errors.acme_eab_hmac}>
            <input id="acme-hmac" type="password" className="input mono max-w-[360px]" autoComplete="off" spellCheck={false} value={hmac} onChange={(e) => setHmac(e.target.value)} />
          </Field>
        </>
      ) : null}
      <FormActions saving={save.isPending} disabled={!dirty} label={t("certs.save")} />
    </form>
  );
}

export function PasswordCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [revokeKeys, setRevokeKeys] = useState(true);
  const change = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/auth/password", { body: { current, new: next, revoke_keys: revokeKeys } })),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      // Revoking the keys takes the passkeys with them.
      if (revokeKeys) void qc.invalidateQueries({ queryKey: qk.passkeys });
      toast.ok(t("settings.passwordChanged"));
    },
    onError: (e) => {
      onFormError(e, toast);
    },
  });
  const errors = fieldErrors(change.error);
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          change.mutate();
        }}
        noValidate
      >
        <div className="card-head">
          <h2 className="card-title">{t("login.password")}</h2>
        </div>
        <Field label={t("settings.currentPassword")} htmlFor="p-cur" error={errors.current}>
          <input id="p-cur" type="password" className="input" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
        </Field>
        <Field label={t("settings.newPassword")} htmlFor="p-new" hint={t("settings.newPasswordHint")} error={errors.new}>
          <input id="p-new" type="password" className="input" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} minLength={12} />
        </Field>
        <label className="mb-4 flex items-start gap-2 text-[13px] text-[var(--ink-600)]">
          <input type="checkbox" className="check mt-0.5 shrink-0" checked={revokeKeys} onChange={(e) => setRevokeKeys(e.target.checked)} />
          <span>{t("settings.revokeKeys")}</span>
        </label>
        <FormActions saving={change.isPending} disabled={!current || next.length < 12} label={t("settings.changePassword")} />
      </form>
    </section>
  );
}

export function TwoFactorCard() {
  const me = useQuery(meQuery);
  const qc = useQueryClient();
  const toast = useToast();
  const [setup, setSetup] = useState<{ secret: string; uri: string } | null>(null);
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [disable, setDisable] = useState(false);
  const [pw, setPw] = useState("");
  const enabled = me.data?.admin.totp_enabled;

  const [startPw, setStartPw] = useState("");
  const start = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/auth/totp/setup", { body: { password: startPw } })),
    onSuccess: (r) => {
      setStartPw("");
      setSetup(r);
    },
  });
  const enable = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/auth/totp/enable", { body: { code } })),
    onSuccess: (r) => {
      setCodes(r.recovery_codes);
      setSetup(null);
      setCode("");
      void qc.invalidateQueries({ queryKey: qk.me });
    },
  });
  const off = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/auth/totp/disable", { body: { password: pw, code } })),
    onSuccess: () => {
      setDisable(false);
      setPw("");
      setCode("");
      void qc.invalidateQueries({ queryKey: qk.me });
      toast.ok(t("settings.twoFactorOffToast"));
    },
    onError: (e) => toast.error(errorText(e)),
  });

  return (
    <section className="card glass reveal" style={{ "--i": 3 } as React.CSSProperties}>
      <CardHead title={t("settings.twoFactor")} sub={enabled ? t("settings.twoFactorOn") : t("settings.twoFactorOff")} aside={<ShieldCheck size={20} className={enabled ? "text-[var(--leaf-500)]" : "text-[var(--ink-300)]"} aria-hidden />} />
      {codes ? (
        <div>
          <p className="mb-3 text-[13px] text-[var(--ink-600)]">{t("settings.recoveryText")}</p>
          <div className="panel-soft mono grid grid-cols-2 gap-2 p-3 text-sm">
            {codes.map((c) => (
              <span key={c}>{c}</span>
            ))}
          </div>
          <div className="form-actions">
            <Button variant="primary" onClick={() => setCodes(null)}>
              {t("settings.recoverySaved")}
            </Button>
          </div>
        </div>
      ) : setup ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            enable.mutate();
          }}
        >
          <div className="grid gap-4 sm:grid-cols-[160px_minmax(0,1fr)]">
            <QR value={setup.uri} size={160} label={t("settings.totpQr")} />
            <div>
              <p className="mb-3 text-[13px] text-[var(--ink-600)]">{t("settings.totpScan")}</p>
              <p className="mono mb-3 text-xs break-all text-[var(--ink-500)]">{setup.secret}</p>
              <Field label={t("settings.totpCode")} htmlFor="totp-code" error={enable.error instanceof ApiError ? (enable.error.fields.code ?? errorText(enable.error)) : undefined}>
                <input id="totp-code" className="input mono max-w-[160px] tracking-[0.2em]" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.trim())} autoComplete="one-time-code" />
              </Field>
            </div>
          </div>
          <div className="form-actions">
            <Button variant="ghost" onClick={() => setSetup(null)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" variant="primary" loading={enable.isPending} disabled={code.length !== 6}>
              {t("common.enable")}
            </Button>
          </div>
        </form>
      ) : enabled ? (
        disable ? (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              off.mutate();
            }}
          >
            <Field label={t("login.password")} htmlFor="off-pw">
              <input id="off-pw" type="password" className="input" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="current-password" />
            </Field>
            <Field label={t("settings.totpCode")} htmlFor="off-code">
              <input id="off-code" className="input mono max-w-[160px]" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.trim())} />
            </Field>
            <div className="form-actions">
              <Button variant="ghost" onClick={() => setDisable(false)}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" variant="danger-solid" loading={off.isPending} disabled={!pw || code.length !== 6}>
                {t("settings.twoFactorDisable")}
              </Button>
            </div>
          </form>
        ) : (
          <Button variant="danger" onClick={() => setDisable(true)}>
            {t("common.disable")}
          </Button>
        )
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            start.mutate();
          }}
          noValidate
        >
          <Field label={t("login.password")} htmlFor="on-pw" hint={t("settings.twoFactorPasswordHint")} error={start.error instanceof ApiError ? (start.error.fields.password ?? errorText(start.error)) : undefined}>
            <input id="on-pw" type="password" className="input max-w-[320px]" value={startPw} onChange={(e) => setStartPw(e.target.value)} autoComplete="current-password" />
          </Field>
          <FormActions saving={start.isPending} disabled={!startPw} label={t("settings.twoFactorEnable")} />
        </form>
      )}
    </section>
  );
}

/**
 * Passkeys: a way in beside the password. Adding one asks for the password (and the code when
 * 2FA is on) before the browser makes the key; the panel needs its domain for them.
 */
export function PasskeysCard({ s }: { s: Schemas["SettingsView"] }) {
  const me = useQuery(meQuery);
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState("");
  const [pw, setPw] = useState("");
  const [code, setCode] = useState("");
  const [deleting, setDeleting] = useState<Schemas["PasskeyView"] | null>(null);
  const totp = !!me.data?.admin.totp_enabled;
  const usable = !!s.domain && passkeysSupported();
  const list = useQuery({ queryKey: qk.passkeys, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/auth/passkeys", { signal })) });
  const add = useMutation({
    mutationFn: async () => {
      const begin = await unwrap(api.POST("/api/v1/auth/passkeys/begin", { body: { password: pw, totp: totp ? code : undefined } }));
      const credential = await createPasskey(begin.options);
      return unwrap(api.POST("/api/v1/auth/passkeys", { body: { name: name.trim() || t("settings.passkeyNameDefault"), credential } }));
    },
    onSuccess: () => {
      setName("");
      setPw("");
      setCode("");
      void qc.invalidateQueries({ queryKey: qk.passkeys });
      toast.ok(t("settings.passkeyAdded"));
    },
    onError: (e) => {
      if (passkeyCancelled(e)) return;
      if (e instanceof ApiError) onFormError(e, toast);
      // The browser refuses a passkey of the panel's domain at another address (its IP).
      else if (e instanceof DOMException && e.name === "SecurityError") toast.error(t("login.passkeyAddress"));
      else toast.error(t("login.passkeyFailed"));
    },
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/auth/passkeys/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      setDeleting(null);
      void qc.invalidateQueries({ queryKey: qk.passkeys });
      toast.ok(t("settings.passkeyDeleted"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const errors = fieldErrors(add.error);
  return (
    <section className="card glass reveal" style={{ "--i": 3 } as React.CSSProperties}>
      <CardHead title={t("settings.passkeys")} sub={t("settings.passkeysSub")} aside={<Fingerprint size={20} className="text-[var(--ink-300)]" aria-hidden />} />
      <QueryBoundary query={list} pending={<Skeleton style={{ height: 48 }} />}>
        {(keys) =>
          keys.length === 0 ? (
            <p className="mb-4 text-[13px] text-[var(--ink-500)]">{t("settings.passkeysNone")}</p>
          ) : (
            <ul className="row-list mb-4">
              {keys.map((k) => (
                <li key={k.id} className="flex items-center justify-between gap-3 py-3">
                  <div className="min-w-0">
                    <div className="truncate text-[13px] font-medium">{k.name}</div>
                    <div className="text-xs text-[var(--ink-500)]">
                      {t("settings.passkeyAddedOn", { date: dateShort(k.created_at) })} · {k.last_used_at ? t("settings.passkeyUsed", { ago: ago(k.last_used_at) }) : t("settings.passkeyNeverUsed")}
                    </div>
                  </div>
                  <Button size="sm" variant="danger" onClick={() => setDeleting(k)}>
                    {t("common.delete")}
                  </Button>
                </li>
              ))}
            </ul>
          )
        }
      </QueryBoundary>
      {!s.domain ? (
        <p className="text-[13px] text-[var(--ink-500)]">{t("settings.passkeyNoDomain")}</p>
      ) : !usable ? (
        <p className="text-[13px] text-[var(--ink-500)]">{t("settings.passkeyUnsupported")}</p>
      ) : (
        <form
          className="border-t border-[var(--hairline)] pt-4"
          onSubmit={(e) => {
            e.preventDefault();
            add.mutate();
          }}
          noValidate
        >
          <Field label={t("settings.passkeyName")} htmlFor="pk-name" error={errors.name}>
            <input id="pk-name" className="input max-w-[320px]" maxLength={60} placeholder={t("settings.passkeyNameDefault")} value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label={t("login.password")} htmlFor="pk-pw" hint={t("settings.passkeyPasswordHint")} error={errors.password}>
            <input id="pk-pw" type="password" className="input max-w-[320px]" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="current-password" />
          </Field>
          {totp ? (
            <Field label={t("settings.totpCode")} htmlFor="pk-code" error={errors.totp}>
              <input id="pk-code" className="input mono max-w-[160px]" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.trim())} autoComplete="one-time-code" />
            </Field>
          ) : null}
          <p className="mb-4 text-xs text-[var(--ink-500)]">{t("settings.passkeyDomainNote")}</p>
          <FormActions saving={add.isPending} disabled={!pw || (totp && code.length !== 6)} label={t("settings.passkeyAdd")} />
        </form>
      )}
      <Confirm
        open={!!deleting}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        title={t("settings.passkeyDeleteTitle")}
        text={t("settings.passkeyDeleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
    </section>
  );
}

export function SessionsCard() {
  const qc = useQueryClient();
  const toast = useToast();
  const sessions = useQuery({ queryKey: qk.sessions, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/auth/sessions", { signal })) });
  const revoke = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/api/v1/auth/sessions/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.sessions });
      toast.ok(t("settings.sessionEnded"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <section className="card glass reveal" style={{ "--i": 4 } as React.CSSProperties}>
      <CardHead title={t("settings.sessions")} sub={t("settings.sessionsSub")} />
      <QueryBoundary query={sessions} pending={<Skeleton style={{ height: 80 }} />}>
        {(list) => (
          <ul className="row-list">
            {list.map((s) => (
              <li key={s.id} className="flex items-center justify-between gap-3 py-3">
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-medium">{browserName(s.user_agent)}</div>
                  <div className="text-xs text-[var(--ink-500)]">
                    <span className="mono">{s.ip}</span> · {s.current ? <span className="text-[var(--leaf-700)]">{t("settings.thisSession")}</span> : t("settings.activeAgo", { ago: ago(s.last_seen_at) })}
                  </div>
                </div>
                {!s.current ? (
                  <Button size="sm" variant="danger" loading={revoke.isPending && revoke.variables === s.id} onClick={() => revoke.mutate(s.id)}>
                    <LogOut size={16} aria-hidden /> {t("settings.endSession")}
                  </Button>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </QueryBoundary>
    </section>
  );
}

function browserName(ua: string): string {
  const os = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac OS X/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
  const br = /Edg\//.test(ua) ? "Edge" : /YaBrowser/.test(ua) ? t("settings.yandexBrowser") : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : t("settings.browser");
  return os ? `${br}, ${os}` : br;
}

// API keys and the reference live on their own page: they are for scripts, not daily work.
export function ApiCard() {
  return (
    <section className="card glass reveal" style={{ "--i": 5 } as React.CSSProperties}>
      <CardHead
        title={t("settings.api")}
        sub={t("settings.apiSub")}
        aside={<Link to="/settings/api" className="btn btn-glass btn-sm">
          {t("settings.apiOpen")} <ChevronRight size={16} aria-hidden />
        </Link>}
      />
    </section>
  );
}
