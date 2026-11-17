import type { Inbound, Schemas } from "../../../api/client";
import { Field } from "../../../components/ui";
import { SwitchRow } from "../../../components/switch";
import { t } from "../../../i18n";
import { Select } from "../../../components/select";

/** The headers a CDN puts the client's address in; Cloudflare's first. */
const HEADERS = ["CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP", "True-Client-IP"] as const;
const CLOUDFLARE = "cloudflare";

/** What the "behind a CDN" block edits: where the apps connect and whose header the node believes. */
export type CdnForm = {
  on: boolean;
  domain: string; // the CDN's domain: the apps' address, TLS name and, by default, Host
  port: string; // "" = 443
  host: string; // "" = the domain
  realIP: boolean;
  header: string;
  cloudflare: boolean; // Cloudflare's networks are trusted
  nets: string; // the CDN's own networks, one a line
};

export function cdnFormOf(inb: Inbound): CdnForm {
  const c = inb.client;
  const trusted = c.cdn?.trusted ?? [];
  return {
    on: !!c.cdn,
    domain: c.cdn ? c.server : "",
    port: c.cdn && c.port ? String(c.port) : "",
    host: c.cdn && c.host && c.host !== c.server ? c.host : "",
    realIP: !!c.cdn?.header,
    header: c.cdn?.header || HEADERS[0],
    cloudflare: c.cdn ? trusted.includes(CLOUDFLARE) || !c.cdn.header : true,
    nets: trusted.filter((s) => s !== CLOUDFLARE).join("\n"),
  };
}

type Client = Schemas["ClientEndpoint"];

/** The client endpoint the form makes, or the field errors that keep it from being one. */
export function cdnClient(f: CdnForm, inb: Inbound): { client: Client } | { errors: Record<string, string> } {
  const domain = f.domain.trim();
  if (!domain) return { errors: { "client.server": t("inbounds.cdn.domainRequired") } };
  const port = f.port.trim() === "" ? 443 : Number(f.port.trim());
  if (!Number.isInteger(port) || port < 1 || port > 65535) return { errors: { "client.port": t("errors.api.config_client_port") } };
  const trusted = [...(f.cloudflare ? [CLOUDFLARE] : []), ...f.nets.split(/[\s,]+/).filter(Boolean)];
  if (f.realIP && trusted.length === 0) return { errors: { "client.cdn.trusted": t("errors.api.cdn_trusted_needed") } };
  return {
    client: {
      server: domain,
      port,
      // The apps greet the CDN by its own name; a node certificate's name would not do.
      sni: inb.client_sni ? domain : "",
      host: inb.client_host ? f.host.trim() || domain : "",
      cdn: f.realIP ? { header: f.header, trusted } : {},
    },
  };
}

/** The "behind a CDN" block of the inbound drawer. */
export function CdnSettings({
  value,
  onChange,
  errors,
  clearError,
  inbound,
  disabled,
}: {
  value: CdnForm;
  onChange: (f: CdnForm) => void;
  errors: Record<string, string>;
  clearError: (key: string) => void;
  inbound: Inbound;
  disabled?: boolean;
}) {
  const set = (patch: Partial<CdnForm>) => onChange({ ...value, ...patch });
  return (
    <>
      <SwitchRow label={t("inbounds.cdn.on")} sub={t("inbounds.cdn.onSub")} checked={value.on} onChange={(on) => set({ on })} disabled={disabled} />
      {value.on ? (
        <div className="mt-4">
          <div className="grid gap-x-4 sm:grid-cols-[1fr_160px]">
            <Field label={t("inbounds.cdn.domain")} htmlFor="ed-cdn-domain" hint={t("inbounds.cdn.domainHint")} error={errors["client.server"]}>
              <input
                id="ed-cdn-domain"
                className="input mono"
                value={value.domain}
                onChange={(e) => {
                  set({ domain: e.target.value });
                  clearError("client.server");
                }}
                placeholder="cdn.example.com"
                aria-invalid={!!errors["client.server"]}
                spellCheck={false}
                autoComplete="off"
                disabled={disabled}
              />
            </Field>
            <Field label={t("inbounds.cdn.port")} htmlFor="ed-cdn-port" error={errors["client.port"]}>
              <input
                id="ed-cdn-port"
                className="input"
                inputMode="numeric"
                value={value.port}
                onChange={(e) => {
                  set({ port: e.target.value });
                  clearError("client.port");
                }}
                placeholder="443"
                aria-invalid={!!errors["client.port"]}
                disabled={disabled}
              />
            </Field>
          </div>
          {inbound.client_host ? (
            <Field label={t("inbounds.cdn.host")} htmlFor="ed-cdn-host" hint={t("inbounds.cdn.hostHint")} error={errors["client.host"]}>
              <input
                id="ed-cdn-host"
                className="input mono"
                value={value.host}
                onChange={(e) => {
                  set({ host: e.target.value });
                  clearError("client.host");
                }}
                placeholder={value.domain.trim() || t("inbounds.cdn.hostDefault")}
                aria-invalid={!!errors["client.host"]}
                spellCheck={false}
                autoComplete="off"
                disabled={disabled}
              />
            </Field>
          ) : null}
          <SwitchRow
            label={t("inbounds.cdn.realIP")}
            sub={value.realIP ? t("inbounds.cdn.realIPOn") : t("inbounds.cdn.realIPOff")}
            warn={!value.realIP}
            checked={value.realIP}
            onChange={(realIP) => set({ realIP })}
            disabled={disabled}
          />
          {value.realIP ? (
            <div className="mt-4">
              <Field label={t("inbounds.cdn.header")} htmlFor="ed-cdn-header" hint={t("inbounds.cdn.headerHint")} error={errors["client.cdn.header"]}>
                <Select id="ed-cdn-header" className="input max-w-[320px]" value={value.header} onChange={(e) => set({ header: e.target.value })} disabled={disabled}>
                  {HEADERS.map((h) => (
                    <option key={h} value={h}>
                      {h}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field label={t("inbounds.cdn.trusted")} htmlFor="ed-cdn-nets" hint={t("inbounds.cdn.trustedHint")} error={errors["client.cdn.trusted"]}>
                <label className="mb-2 flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    className="check shrink-0"
                    checked={value.cloudflare}
                    onChange={(e) => {
                      set({ cloudflare: e.target.checked });
                      clearError("client.cdn.trusted");
                    }}
                    disabled={disabled}
                  />
                  {t("inbounds.cdn.cloudflare")}
                </label>
                <textarea
                  id="ed-cdn-nets"
                  className="input mono min-h-[72px]"
                  value={value.nets}
                  onChange={(e) => {
                    set({ nets: e.target.value });
                    clearError("client.cdn.trusted");
                  }}
                  placeholder={"203.0.113.0/24\n2001:db8::/32"}
                  aria-invalid={!!errors["client.cdn.trusted"]}
                  spellCheck={false}
                  disabled={disabled}
                />
              </Field>
            </div>
          ) : null}
          <p className="text-xs text-[var(--ink-500)]" role="note">
            {t("inbounds.cdn.note")}
          </p>
        </div>
      ) : null}
    </>
  );
}
