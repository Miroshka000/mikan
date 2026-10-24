// The DNS at the exit: the nodes look a site up themselves, so a site sees the node's
// country whatever DNS the user's device asked. On from the start; the routing's DNS part
// tunes it and saves it with the rest of the routing.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "../../../api/client";
import { qk } from "../../../api/hooks";
import { SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { fieldErrors, onFormError } from "../../../lib/fields";
import { splitList } from "../../../lib/list";
import { Select } from "../../../components/select";

export type ExitDnsInfo = Schemas["ExitDNSInfo"];
type Resolver = ExitDnsInfo["resolver"];
export type ExitDnsDraft = { sniff: boolean; resolver: Resolver; servers: string };

const RESOLVERS: Resolver[] = ["host", "cloudflare", "google", "quad9", "custom"];


export const exitDnsDraftOf = (v: ExitDnsInfo): ExitDnsDraft => ({ sniff: v.sniff, resolver: v.resolver, servers: v.servers.join("\n") });

/** Saves the DNS at the exit; a wrong server is shown under the list, anything else is a toast. */
export function useSaveExitDns() {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: (d: ExitDnsDraft) => unwrap(api.PUT("/api/v1/exit-dns", { body: { sniff: d.sniff, resolver: d.resolver, servers: splitList(d.servers) } })),
    onSuccess: (nv) => {
      qc.setQueryData(qk.exitDns, nv);
      toast.ok(t("settings.exitDns.saved"));
    },
    onError: (e) => onFormError(e, toast),
  });
}

/** The fields of the DNS at the exit, inside the routing's form. */
export function ExitDnsFields({ v, draft, setDraft, error }: { v: ExitDnsInfo; draft: ExitDnsDraft; setDraft: (f: (d: ExitDnsDraft) => ExitDnsDraft) => void; error: unknown }) {
  const errors = fieldErrors(error);
  const serversError = Object.entries(errors).find(([k]) => k === "servers" || k.startsWith("servers["))?.[1];
  const preset = v.presets[draft.resolver];
  const off = !draft.sniff && draft.resolver === "host";
  return (
    <>
      <SwitchRow label={t("settings.exitDns.sniff")} sub={t("settings.exitDns.sniffSub")} checked={draft.sniff} onChange={(sniff) => setDraft((d) => ({ ...d, sniff }))} />
      <Field label={t("settings.exitDns.resolver")} htmlFor="exit-dns-resolver" hint={preset ? preset.join(", ") : draft.resolver === "host" ? t("settings.exitDns.resolverHint") : undefined}>
        <Select id="exit-dns-resolver" className="input max-w-[320px]" value={draft.resolver} onChange={(e) => setDraft((d) => ({ ...d, resolver: e.target.value as Resolver }))}>
          {RESOLVERS.map((r) => (
            <option key={r} value={r}>
              {t(`settings.exitDns.resolvers.${r}`)}
            </option>
          ))}
        </Select>
      </Field>
      {draft.resolver === "custom" ? (
        <Field label={t("settings.exitDns.servers")} htmlFor="exit-dns-servers" hint={t("settings.exitDns.serversHint")} error={serversError}>
          <textarea
            id="exit-dns-servers"
            className="input mono min-h-[96px]"
            value={draft.servers}
            onChange={(e) => setDraft((d) => ({ ...d, servers: e.target.value }))}
            spellCheck={false}
            placeholder={"https://1.1.1.1/dns-query\ntls://dns.google"}
          />
        </Field>
      ) : null}
      {off ? (
        <p className="mb-3 text-[13px] text-[var(--ink-600)]" role="status">
          {t("settings.exitDns.off")}
        </p>
      ) : null}
    </>
  );
}
