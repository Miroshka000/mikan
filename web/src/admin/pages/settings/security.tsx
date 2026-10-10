import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight, Copy, KeyRound, LoaderCircle, LogOut, RefreshCw, ShieldCheck, Stethoscope } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../../api/client";
import { meQuery, qk } from "../../../api/hooks";
import { FormActions } from "../../../components/layout";
import { Confirm } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { useToast } from "../../../components/toast";
import { Button, Field, Pill, QR, Segmented, Skeleton } from "../../../components/ui";
import { AcmeAttempts, AcmeProblem, caName, CertCheckDrawer } from "../../../components/acme";
import { getLocale, t, tMaybe } from "../../../i18n";
import { useCopy } from "../../../lib/copy";
import { CertDrawer, certUntil, type CertInfo } from "../../../components/cert-drawer";
import { fieldErrors } from "../../../lib/fields";
import { ago } from "../../../lib/format";

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
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.adminLink")}</h2>
          <div className="card-sub">{t("settings.adminLinkSub")}</div>
        </div>
      </div>
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
  const qc = useQueryClient();
  const toast = useToast();
  const c = s.certificate;
  // The outcome of "Get now" shows here, under the button, not only as a toast.
  const [outcome, setOutcome] = useState<"got" | "running" | null>(null);
  const renew = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/settings/certificate/renew")),
    onMutate: () => setOutcome(null),
    onSuccess: (st) => {
      qc.setQueryData<Schemas["SettingsView"]>(qk.settings, (old) => (old ? { ...old, certificate: st } : old));
      setOutcome(st.ordering ? "running" : st.error ? null : "got");
      if (st.ordering) window.setTimeout(() => void qc.invalidateQueries({ queryKey: qk.settings }), 15_000);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const [own, setOwn] = useState(false);
  const [checking, setChecking] = useState(false);
  const [caFocus, setCaFocus] = useState(0);
  const custom = c.kind === "custom";
  const ok = c.kind === "acme" || custom;
  const until = new Date(c.not_after).toLocaleString(getLocale(), { day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" });
  const sub = custom ? t("settings.certCustom", { names: (c.names ?? []).join(", "), until: certUntil(c.not_after) }) : ok ? t("certs.issued", { ca: caName(c.ca), id: c.identifier, until }) : t("settings.certSelf");
  // The own certificate as the drawer shows it; a broken one is shown by its error.
  const current: CertInfo | null = custom ? { names: c.names, issuer: c.issuer, not_after: c.not_after, trusted: c.trusted } : c.error?.startsWith("custom_") ? { error: c.error } : null;
  const switching = c.kind === "acme" && !!c.ca && c.ca !== c.ca_wanted;
  const failed = !!c.error && !c.error.startsWith("custom_") && c.error !== "no_public_host";
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.cert")}</h2>
          <div className="card-sub">{sub}</div>
        </div>
        {custom ? <Pill tone="ok">{t("settings.certOwn")}</Pill> : ok ? <Pill tone="ok">{t("settings.certValid")}</Pill> : <Pill tone="warn">{t("settings.certTemp")}</Pill>}
      </div>
      {c.ordering || renew.isPending ? (
        <p className="mb-3 flex items-center gap-2 text-[13px] text-[var(--ink-600)]" role="status">
          <LoaderCircle size={16} className="spin" aria-hidden /> {t("certs.ordering")}
        </p>
      ) : c.error ? (
        c.error.startsWith("custom_") ? (
          <p className="mb-3 text-[13px] text-[var(--berry-600)]" role="alert">
            {tMaybe(`errors.acme.${c.error}`) ?? c.error}
          </p>
        ) : (
          <AcmeProblem code={c.error} detail={c.error_detail} holder={c.holder} retryAt={c.retry_at} host={c.identifier} className="mb-3" />
        )
      ) : outcome ? (
        <p className="mb-3 text-[13px] text-[var(--leaf-700)]" role="status">
          {outcome === "got" ? t("certs.got") : t("certs.stillRunning")}
        </p>
      ) : switching ? (
        <p className="mb-3 text-[13px] text-[var(--ink-600)]" role="status">
          {t("certs.switching", { ca: caName(c.ca_wanted) })}
        </p>
      ) : null}
      <p className="mb-3 text-xs text-[var(--ink-500)]">{custom ? t("settings.certOwnNote") : t("settings.certNote")}</p>
      <AcmeAttempts attempts={c.attempts} className="mb-3" />
      <div className="flex flex-wrap gap-2">
        {!custom && c.error !== "no_public_host" ? (
          <Button size="sm" loading={renew.isPending} disabled={renew.isPending || c.ordering} onClick={() => renew.mutate()}>
            <RefreshCw size={16} aria-hidden /> {failed ? t("certs.retry") : t("certs.getNow")}
          </Button>
        ) : null}
        <Button size="sm" onClick={() => setChecking(true)}>
          <Stethoscope size={16} aria-hidden /> {t("certs.check")}
        </Button>
        <Button size="sm" onClick={() => setOwn(true)}>
          <ShieldCheck size={16} aria-hidden /> {custom ? t("settings.certReplace") : t("settings.certOwnButton")}
        </Button>
      </div>
      <AuthorityForm s={s} focus={caFocus} />
      <CertCheckDrawer
        open={checking}
        onClose={() => setChecking(false)}
        onUseZeroSSL={() => setCaFocus((n) => n + 1)}
      />
      <CertDrawer
        open={own}
        onClose={() => setOwn(false)}
        title={t("cert.panelTitle")}
        lead={t("cert.panelLead")}
        current={current}
        save={(cert, key) => unwrap(api.PUT("/api/v1/settings/certificate", { body: { cert, key } })).then((v) => qc.setQueryData(qk.settings, v))}
        clear={() =>
          unwrap(api.DELETE("/api/v1/settings/certificate")).then(() => {
            void qc.invalidateQueries({ queryKey: qk.settings });
            window.setTimeout(() => void qc.invalidateQueries({ queryKey: qk.settings }), 15_000);
          })
        }
        clearLabel={t("cert.panelClear")}
        clearText={t("cert.panelClearText")}
      />
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
      if (!(e instanceof ApiError && Object.keys(e.fields).length)) toast.error(errorText(e));
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
  const toast = useToast();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [revokeKeys, setRevokeKeys] = useState(true);
  const change = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/auth/password", { body: { current, new: next, revoke_keys: revokeKeys } })),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      toast.ok(t("settings.passwordChanged"));
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
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.twoFactor")}</h2>
          <div className="card-sub">{enabled ? t("settings.twoFactorOn") : t("settings.twoFactorOff")}</div>
        </div>
        <ShieldCheck size={20} className={enabled ? "text-[var(--leaf-500)]" : "text-[var(--ink-300)]"} aria-hidden />
      </div>
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
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.sessions")}</h2>
          <div className="card-sub">{t("settings.sessionsSub")}</div>
        </div>
      </div>
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
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.api")}</h2>
          <div className="card-sub">{t("settings.apiSub")}</div>
        </div>
        <Link to="/settings/api" className="btn btn-glass btn-sm">
          {t("settings.apiOpen")} <ChevronRight size={16} aria-hidden />
        </Link>
      </div>
    </section>
  );
}
