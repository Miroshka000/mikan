import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk } from "../../../api/hooks";
import { Confirm } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Button, Pill } from "../../../components/ui";
import { t } from "../../../i18n";
import { dateShort } from "../../../lib/format";
import type { CertRefusal } from "./cert-refusal";

type Former = Schemas["FormerStatus"];

/** What a refusal says, with a retry and a save that does not wait for the certificate. */
export function CertRefusalNote({ r, retrying, forcing, onRetry, onForce }: { r: CertRefusal; retrying: boolean; forcing: boolean; onRetry: () => void; onForce: () => void }) {
  return (
    <div className="banner warn mb-3 flex-col items-start gap-1 text-xs" role="alert">
      <p>{r.code === "cert_ordering" ? t("settings.certOrdering") : t("settings.certNotReady", { reason: r.reason || t("errors.acme.acme_failed") })}</p>
      <p className="mt-1 text-[var(--ink-500)]">{t("settings.saveWithoutCertHint")}</p>
      <div className="mt-2 flex flex-wrap gap-2">
        <Button size="sm" loading={retrying} disabled={forcing} onClick={onRetry}>
          {t("settings.certRetry")}
        </Button>
        <Button size="sm" variant="ghost" loading={forcing} disabled={retrying} onClick={onForce}>
          {t("settings.saveWithoutCert")}
        </Button>
      </div>
    </div>
  );
}

/**
 * The names the panel answered at before its domain changed: the links, webhooks and node
 * keys handed out with them keep working while they lead here. Each can be let go by hand.
 */
export function FormerDomains({ list }: { list: Former[] }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [ask, setAsk] = useState<Former | null>(null);
  const remove = useMutation({
    mutationFn: (name: string) => unwrap(api.DELETE("/api/v1/settings/former-domains/{name}", { params: { path: { name } } })),
    onSuccess: (data) => {
      qc.setQueryData(qk.settings, data);
      setAsk(null);
      toast.ok(t("settings.saved"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  if (list.length === 0) return null;
  return (
    <div className="mt-4 border-t border-[var(--line)] pt-4">
      <h3 className="text-[13px] font-semibold">{t("settings.former.title")}</h3>
      <p className="mb-3 mt-1 text-xs text-[var(--ink-500)]">{t("settings.former.sub")}</p>
      <ul className="row-list">
        {list.map((f) => (
          <li key={f.name} className="flex flex-wrap items-center justify-between gap-2 py-2">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="mono break-all text-[13px]">{f.name}</span>
                {f.sub ? <Pill tone="off">{t("settings.former.subscriptions")}</Pill> : null}
                {f.kind === "own" ? <Pill tone="ok">{t("settings.former.works")}</Pill> : <Pill tone="warn">{t("settings.former.noCert")}</Pill>}
              </div>
              <p className="mt-1 text-xs text-[var(--ink-500)]">
                {[
                  t("settings.former.since", { date: dateShort(f.since) }),
                  f.not_after ? t("settings.former.certUntil", { date: dateShort(f.not_after) }) : "",
                  f.drop_at ? t("settings.former.dropAt", { date: dateShort(f.drop_at) }) : "",
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </p>
            </div>
            <Button size="sm" variant="ghost" onClick={() => setAsk(f)} disabled={remove.isPending}>
              {t("settings.former.remove")}
            </Button>
          </li>
        ))}
      </ul>
      <Confirm
        open={!!ask}
        onOpenChange={(v) => !v && setAsk(null)}
        title={ask ? t("settings.former.removeTitle", { name: ask.name }) : ""}
        text={t("settings.former.removeText")}
        confirm={t("settings.former.removeConfirm")}
        danger
        loading={remove.isPending}
        onConfirm={() => ask && remove.mutate(ask.name)}
      />
    </div>
  );
}
