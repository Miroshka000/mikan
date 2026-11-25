import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, LogIn } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../../api/client";
import { meQuery, qk } from "../../../api/hooks";
import { CardHead, FormActions } from "../../../components/layout";
import { Confirm } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { useToast } from "../../../components/toast";
import { Button, Field, Skeleton } from "../../../components/ui";
import { t, tMaybe } from "../../../i18n";
import { useCopy } from "../../../lib/copy";
import { fieldErrors, onFormError } from "../../../lib/fields";
import { ago, dateShort } from "../../../lib/format";

type Provider = Schemas["IdentityView"]["provider"];
const PROVIDERS: Provider[] = ["github", "telegram"];

function Part({ bare, i, children }: { bare?: boolean; i: number; children: React.ReactNode }) {
  return bare ? (
    <div className="login-part">{children}</div>
  ) : (
    <section className="card glass reveal" style={{ "--i": i } as React.CSSProperties}>
      {children}
    </section>
  );
}

/** What GitHub or Telegram sent the browser back with, in the address's fragment; read once and taken out of the address. */
function takeFragment(): URLSearchParams | null {
  const hash = window.location.hash.slice(1);
  if (!/^(identity|github_error|tgAuthResult)=/.test(hash)) return null;
  window.history.replaceState(null, "", window.location.pathname + window.location.search);
  return new URLSearchParams(hash);
}

/**
 * Accounts of GitHub and Telegram the admin signs in with. Linking one asks for the password
 * (and the code when 2FA is on), then the browser goes to the service and comes back here.
 */
export function IdentitiesCard({ bare }: { bare?: boolean }) {
  const me = useQuery(meQuery);
  const qc = useQueryClient();
  const toast = useToast();
  const copy = useCopy();
  const [pw, setPw] = useState("");
  const [code, setCode] = useState("");
  const [deleting, setDeleting] = useState<Schemas["IdentityView"] | null>(null);
  const [clientID, setClientID] = useState<string | null>(null);
  const [secret, setSecret] = useState("");
  const totp = !!me.data?.admin.totp_enabled;
  const list = useQuery({ queryKey: qk.identities, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/auth/identities", { signal })) });
  const providers = useQuery({ queryKey: qk.identityProviders, queryFn: ({ signal }) => unwrap(api.GET("/api/v1/auth/identity-providers", { signal })) });

  const linkTelegram = useMutation({
    mutationFn: (result: string) => unwrap(api.POST("/api/v1/auth/identities/telegram", { body: { result } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.identities });
      toast.ok(t("settings.identityLinked", { provider: t("settings.identityProvider.telegram") }));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  // Back from GitHub or Telegram: what came of the link is in the address, once.
  const taken = useRef(false);
  useEffect(() => {
    if (taken.current) return;
    taken.current = true;
    const back = takeFragment();
    if (!back) return;
    const result = back.get("tgAuthResult");
    if (result) linkTelegram.mutate(result);
    else if (back.get("identity")) {
      void qc.invalidateQueries({ queryKey: qk.identities });
      toast.ok(t("settings.identityLinked", { provider: t("settings.identityProvider.github") }));
    } else {
      const err = back.get("github_error") ?? "";
      toast.error(tMaybe(`errors.api.${err}`) ?? t("settings.identityFailed"));
    }
  }, [linkTelegram, qc, toast]);

  const link = useMutation({
    mutationFn: async (provider: Provider) => {
      const body = { password: pw, totp: totp ? code : undefined };
      const r =
        provider === "github"
          ? await unwrap(api.POST("/api/v1/auth/identities/github/begin", { body }))
          : await unwrap(api.POST("/api/v1/auth/identities/telegram/begin", { body }));
      window.location.assign(r.url);
    },
    onError: (e) => (e instanceof ApiError ? onFormError(e, toast) : toast.error(errorText(e))),
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/auth/identities/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      setDeleting(null);
      void qc.invalidateQueries({ queryKey: qk.identities });
      toast.ok(t("settings.identityDeleted"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const saveApp = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT("/api/v1/auth/identity-providers/github", {
          body: { client_id: (clientID ?? providers.data?.github.client_id ?? "").trim(), client_secret: secret ? secret.trim() : undefined },
        }),
      ),
    onSuccess: (p) => {
      qc.setQueryData(qk.identityProviders, p);
      setClientID(null);
      setSecret("");
      toast.ok(t("settings.saved"));
    },
    onError: (e) => (e instanceof ApiError ? onFormError(e, toast) : toast.error(errorText(e))),
  });

  const errors = fieldErrors(link.error);
  const appErrors = fieldErrors(saveApp.error);
  const linked = new Set((list.data ?? []).map((i) => i.provider));
  const p = providers.data;
  const ready: Record<Provider, boolean> = {
    github: !!p?.github.client_id && !!p.github.secret_set,
    telegram: !!p?.telegram.bot && !!p.telegram.domain,
  };
  const linkable = PROVIDERS.filter((x) => !linked.has(x));

  return (
    <Part bare={bare} i={5}>
      <CardHead title={t("settings.identities")} sub={t("settings.identitiesSub")} aside={<LogIn size={20} className="text-[var(--ink-300)]" aria-hidden />} />
      <QueryBoundary query={list} pending={<Skeleton style={{ height: 48 }} />}>
        {(rows) =>
          rows.length === 0 ? (
            <p className="mb-4 text-[13px] text-[var(--ink-500)]">{t("settings.identitiesNone")}</p>
          ) : (
            <ul className="row-list mb-4">
              {rows.map((r) => (
                <li key={r.id} className="flex items-center justify-between gap-3 py-3">
                  <div className="min-w-0">
                    <div className="truncate text-[13px] font-medium">
                      {t(`settings.identityProvider.${r.provider}`)} · {r.name || "—"}
                    </div>
                    <div className="text-xs text-[var(--ink-500)]">
                      {t("settings.passkeyAddedOn", { date: dateShort(r.created_at) })} · {r.last_used_at ? t("settings.passkeyUsed", { ago: ago(r.last_used_at) }) : t("settings.passkeyNeverUsed")}
                    </div>
                  </div>
                  <Button size="sm" variant="danger" onClick={() => setDeleting(r)}>
                    {t("settings.identityUnlink")}
                  </Button>
                </li>
              ))}
            </ul>
          )
        }
      </QueryBoundary>

      {linkable.length > 0 ? (
        <form
          className="border-t border-[var(--hairline)] pt-4"
          onSubmit={(e) => {
            e.preventDefault();
          }}
          noValidate
        >
          <Field label={t("login.password")} htmlFor="id-pw" hint={t("settings.identityPasswordHint")} error={errors.password}>
            <input id="id-pw" type="password" className="input max-w-[320px]" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="current-password" />
          </Field>
          {totp ? (
            <Field label={t("settings.totpCode")} htmlFor="id-code" error={errors.totp}>
              <input id="id-code" className="input mono max-w-[160px]" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.trim())} autoComplete="one-time-code" />
            </Field>
          ) : null}
          <div className="flex flex-wrap gap-2">
            {linkable.map((x) => (
              <Button
                key={x}
                type="button"
                variant="glass"
                loading={link.isPending && link.variables === x}
                disabled={!ready[x] || !pw || (totp && code.length !== 6) || link.isPending}
                onClick={() => link.mutate(x)}
              >
                {t(`settings.identityLink.${x}`)}
              </Button>
            ))}
          </div>
          {linkable.some((x) => !ready[x]) ? <p className="mt-2 text-xs text-[var(--ink-500)]">{t("settings.identityNotReady")}</p> : null}
        </form>
      ) : null}

      <details className="mt-4 border-t border-[var(--hairline)] pt-4">
        <summary className="cursor-pointer text-[13px] font-medium">{t("settings.identitySetup")}</summary>
        <QueryBoundary query={providers} pending={<Skeleton style={{ height: 96 }} />}>
          {(pv) => (
            <div className="mt-3 space-y-4">
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  saveApp.mutate();
                }}
                noValidate
              >
                <p className="mb-3 text-xs text-[var(--ink-500)]">{t("settings.githubAppHint")}</p>
                {pv.github.callback_url ? (
                  <Field label={t("settings.githubCallback")}>
                    <div className="flex max-w-full items-center gap-2">
                      <code className="mono min-w-0 truncate text-xs">{pv.github.callback_url}</code>
                      <Button size="sm" variant="glass" type="button" aria-label={t("certs.copy")} onClick={() => void copy(pv.github.callback_url, t("settings.githubCallbackCopied"))}>
                        <Copy size={14} aria-hidden />
                      </Button>
                    </div>
                  </Field>
                ) : null}
                <Field label="Client ID" htmlFor="gh-id" error={appErrors.client_id}>
                  <input id="gh-id" className="input mono max-w-[320px]" maxLength={64} value={clientID ?? pv.github.client_id} onChange={(e) => setClientID(e.target.value)} autoComplete="off" />
                </Field>
                <Field label="Client secret" htmlFor="gh-secret" hint={pv.github.secret_set ? t("settings.githubSecretSet") : undefined} error={appErrors.client_secret}>
                  <input id="gh-secret" type="password" className="input mono max-w-[320px]" maxLength={128} value={secret} onChange={(e) => setSecret(e.target.value)} autoComplete="new-password" />
                </Field>
                <FormActions saving={saveApp.isPending} disabled={clientID === null && !secret} label={t("common.save")} />
              </form>
              <div>
                <div className="mb-1 text-[13px] font-medium">{t("settings.identityProvider.telegram")}</div>
                <p className="text-xs text-[var(--ink-500)]">
                  {!pv.telegram.bot
                    ? t("settings.telegramLoginNoBot")
                    : !pv.telegram.domain
                      ? t("settings.telegramLoginNoDomain")
                      : t("settings.telegramLoginHint", { bot: pv.telegram.bot, domain: pv.telegram.domain })}
                </p>
              </div>
            </div>
          )}
        </QueryBoundary>
      </details>

      <Confirm
        open={!!deleting}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        title={t("settings.identityDeleteTitle")}
        text={t("settings.identityDeleteText")}
        confirm={t("settings.identityUnlink")}
        danger
        loading={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id)}
      />
    </Part>
  );
}
