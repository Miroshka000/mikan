import type { FormEvent } from "react";
import { api, unwrap, type Schemas } from "../../../api/client";
import { certLine, CertPill, CertStatusBlock } from "../../../components/acme";
import { CardHead, FormActions } from "../../../components/layout";
import { Button, Field, Pill } from "../../../components/ui";
import { t } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors } from "../../../lib/fields";
import { certRefusal } from "./cert-refusal";
import { CertRefusalNote } from "./former-domains";
import { useSaveSettings } from "./shared";

type Settings = Schemas["SettingsView"];

// A domain of its own for subscription links: the panel's address stays out of every link
// and the links survive a move of the panel. Requests to it reach the subscriptions alone.
export function SubDomainCard({ s }: { s: Settings }) {
  const save = useSaveSettings();
  const { draft, setDraft } = useDraft(s.sub_domain);
  const refusal = certRefusal(save.error, ["sub_domain"]);
  const error = refusal ? undefined : fieldErrors(save.error).sub_domain;
  const next = draft.trim().toLowerCase();
  const changed = next !== s.sub_domain;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (changed) save.mutate({ sub_domain: next });
  };
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <CardHead title={t("settings.subDomain.title")} sub={t("settings.subDomain.sub")} aside={s.sub_domain ? <Pill tone="ok">{s.sub_domain}</Pill> : null} />
        <Field
          label={t("settings.subDomain.field")}
          htmlFor="s-sub-domain"
          hint={t("settings.subDomain.hint", { domain: s.domain || s.public_host || "—" })}
          error={error}
        >
          <input
            id="s-sub-domain"
            className="input mono max-w-[360px]"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="sub.example.com"
            aria-invalid={!!error}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
        <p className="text-xs text-[var(--ink-500)]">{t("settings.subDomain.note")}</p>
        {refusal && changed ? (
          <div className="mt-3">
            <CertRefusalNote
              r={refusal}
              retrying={save.isPending && !save.variables?.force_domain}
              forcing={save.isPending && !!save.variables?.force_domain}
              onRetry={() => save.mutate({ sub_domain: next })}
              onForce={() => save.mutate({ sub_domain: next, force_domain: true })}
            />
          </div>
        ) : null}
        <FormActions saving={save.isPending && save.variables?.sub_domain === next} disabled={!changed}>
          {s.sub_domain ? (
            <Button variant="ghost" loading={save.isPending && save.variables?.sub_domain === ""} onClick={() => save.mutate({ sub_domain: "" })}>
              {t("settings.subDomain.off")}
            </Button>
          ) : null}
        </FormActions>
      </form>
      {s.sub_domain && s.sub_certificate ? <SubCertificate s={s} c={s.sub_certificate} /> : null}
    </section>
  );
}

/** The subscription domain's own certificate: how it is, get it now, one of your own. */
function SubCertificate({ s, c }: { s: Settings; c: Schemas["Status"] }) {
  return (
    <div className="mt-5 border-t border-[var(--line)] pt-4">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-[13px] font-semibold">{t("settings.subDomain.cert")}</h3>
        <CertPill c={c} pending={t("settings.subDomain.certPending")} />
      </div>
      <p className="mb-3 text-xs text-[var(--ink-600)]">{certLine(c, t("settings.subDomain.certNone"))}</p>
      <CertStatusBlock
        c={c}
        field="sub_certificate"
        renew={() => unwrap(api.POST("/api/v1/settings/sub-certificate/renew"))}
        save={(cert, key) => unwrap(api.PUT("/api/v1/settings/sub-certificate", { body: { cert, key } }))}
        clear={() => unwrap(api.DELETE("/api/v1/settings/sub-certificate"))}
        drawer={{
          title: t("settings.subDomain.drawerTitle"),
          meta: s.sub_domain,
          lead: t("settings.subDomain.drawerLead", { domain: s.sub_domain }),
          clearLabel: t("cert.panelClear"),
          clearText: t("settings.subDomain.clearText"),
        }}
      />
    </div>
  );
}
