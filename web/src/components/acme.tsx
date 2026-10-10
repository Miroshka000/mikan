// Public certificates in plain words: why one was not issued and what to do, and the
// "Check the certificate" drawer with a button or a command for every finding.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Copy, Info, RefreshCw, XCircle } from "lucide-react";
import { useEffect, type ReactNode } from "react";
import { api, errorText, unwrap, type Schemas } from "../api/client";
import { qk } from "../api/hooks";
import { getLocale, t, tMaybe } from "../i18n";
import { useCopy } from "../lib/copy";
import { certUntil } from "./cert-drawer";
import { Drawer } from "./overlay";
import { useToast } from "./toast";
import { Button, ErrorState, Pill, Skeleton } from "./ui";

type Check = Schemas["CertCheck"];
type Report = Schemas["CertReport"];

/** The CA's name for a code of the API ("letsencrypt"). */
export function caName(ca?: string): string {
  switch (ca) {
    case "zerossl":
      return t("certs.caZerossl");
    case "google":
      return t("certs.caGoogle");
    default:
      return t("certs.caLetsencrypt");
  }
}

function when(iso?: string): string {
  return iso ? new Date(iso).toLocaleString(getLocale(), { day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" }) : "";
}

/** Why a certificate was not issued: the text for the code, the program on port 80, when the
 * CA lets an order in again, and the CA's own words folded under "More". */
export function AcmeProblem({ code, detail, holder, retryAt, className }: { code: string; detail?: string; holder?: string; retryAt?: string | null; className?: string }) {
  const text = tMaybe(`errors.acme.${code}`) ?? t("errors.acme.acme_failed");
  return (
    <div className={className} role="alert">
      <p className="text-[13px] text-[var(--berry-600)]">{text}</p>
      {holder ? <p className="mt-1 text-xs text-[var(--ink-600)]">{t("certs.holder", { holder })}</p> : null}
      {retryAt ? <p className="mt-1 text-xs text-[var(--ink-600)]">{t("certs.retryAt", { at: when(retryAt) })}</p> : null}
      <Details text={detail ?? (tMaybe(`errors.acme.${code}`) ? "" : code)} />
    </div>
  );
}

/** Raw words of the CA or the system, folded. */
function Details({ text }: { text?: string }) {
  if (!text) return null;
  return (
    <details className="mt-1 text-xs text-[var(--ink-500)]">
      <summary className="cursor-pointer font-medium text-[var(--ink-600)]">{t("certs.more")}</summary>
      <p className="mono mt-1 break-all whitespace-pre-wrap">{text}</p>
    </details>
  );
}

/** A command to run on a server, with a copy button. */
export function CommandToCopy({ command, label }: { command: string; label: string }) {
  const copy = useCopy();
  return (
    <div className="mt-2">
      <p className="mb-1 text-xs text-[var(--ink-500)]">{label}</p>
      <div className="link-field">
        <span className="mono text-xs">{command}</span>
        <button type="button" className="icon-btn" onClick={() => void copy(command, t("certs.copied"))} aria-label={`${t("certs.copy")}: ${command}`}>
          <Copy size={16} aria-hidden />
        </button>
      </div>
    </div>
  );
}

const ICON: Record<string, ReactNode> = {
  ok: <CheckCircle2 size={18} className="shrink-0 text-[var(--leaf-500)]" aria-hidden />,
  info: <Info size={18} className="shrink-0 text-[var(--ink-400)]" aria-hidden />,
  warn: <AlertTriangle size={18} className="shrink-0 text-[var(--mikan-600)]" aria-hidden />,
  fail: <XCircle size={18} className="shrink-0 text-[var(--berry-600)]" aria-hidden />,
};

/** The text of a finding: its code with its parameters, dates in the reader's words. */
function checkText(c: Check): string {
  const p: Record<string, string> = { ...(c.params ?? {}) };
  if (p.until) p.until = certUntil(p.until);
  if (p.ca) p.ca = caName(p.ca);
  if (p.want) p.want = caName(p.want);
  return tMaybe(`certCheck.${c.code}`, p) ?? c.code;
}

/** "Check the certificate": what apps see from outside, with a fix for each problem. */
export function CertCheckDrawer({ open, onClose, onUseZeroSSL }: { open: boolean; onClose: () => void; onUseZeroSSL: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const run = useMutation({ mutationFn: () => unwrap(api.POST("/api/v1/settings/certificate/check")) });
  const { mutate, reset } = run;
  useEffect(() => {
    if (open) mutate();
    else reset();
  }, [open, mutate, reset]);
  const after = () => {
    void qc.invalidateQueries({ queryKey: qk.settings });
    void qc.invalidateQueries({ queryKey: qk.nodes });
    run.mutate();
  };
  const renew = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/settings/certificate/renew")),
    onSuccess: (st) => {
      if (st.error) toast.error(tMaybe(`errors.acme.${st.error}`) ?? t("errors.acme.acme_failed"));
      else if (st.ordering) toast.ok(t("certs.stillRunning"));
      else toast.ok(t("certs.got"));
      after();
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const renewNode = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/nodes/{id}/certificate/renew", { params: { path: { id } } })),
    onSuccess: (v) => {
      const err = v.acme?.error;
      if (err) toast.error(tMaybe(`errors.acme.${err}`) ?? t("errors.acme.acme_failed"));
      else if (v.acme?.ordering) toast.ok(t("certs.stillRunning"));
      else toast.ok(t("certs.got"));
      after();
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const update = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/nodes/{id}/update", { params: { path: { id } } })),
    onSuccess: () => {
      toast.ok(t("certs.updateAsked"));
      void qc.invalidateQueries({ queryKey: qk.nodes });
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const report: Report | undefined = run.data;
  const fails = report?.checks.filter((c) => c.status === "fail").length ?? 0;
  const warns = report?.checks.filter((c) => c.status === "warn").length ?? 0;

  const fix = (c: Check) => {
    const f = c.fix;
    if (!f) return null;
    switch (f.action) {
      case "renew":
        return (
          <Button size="sm" loading={renew.isPending} disabled={renew.isPending} onClick={() => renew.mutate()}>
            <RefreshCw size={16} aria-hidden /> {c.status === "fail" ? t("certs.retry") : t("certs.getNow")}
          </Button>
        );
      case "renew_node":
        return (
          <Button size="sm" loading={renewNode.isPending && renewNode.variables === f.node_id} disabled={renewNode.isPending} onClick={() => f.node_id && renewNode.mutate(f.node_id)}>
            <RefreshCw size={16} aria-hidden /> {c.status === "fail" ? t("certs.retry") : t("certs.getNow")}
          </Button>
        );
      case "update_node":
        return (
          <Button size="sm" loading={update.isPending && update.variables === f.node_id} disabled={update.isPending} onClick={() => f.node_id && update.mutate(f.node_id)}>
            {t("certs.updateNode")}
          </Button>
        );
      case "use_zerossl":
        return (
          <Button
            size="sm"
            onClick={() => {
              onClose();
              onUseZeroSSL();
            }}
          >
            {t("certs.useZerossl")}
          </Button>
        );
      case "copy":
        return f.command ? <CommandToCopy command={f.command} label={c.node_id ? t("certs.onNode") : t("certs.onServer")} /> : null;
    }
    return null;
  };

  return (
    <Drawer
      open={open}
      onOpenChange={(v) => !v && onClose()}
      title={t("certs.checkTitle")}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button variant="primary" loading={run.isPending} disabled={run.isPending} onClick={() => run.mutate()}>
            <RefreshCw size={16} aria-hidden /> {t("certs.checkAgain")}
          </Button>
        </>
      }
    >
      <p className="pt-5 text-[13px] text-[var(--ink-600)]">{t("certs.checkLead")}</p>
      {run.isPending ? (
        <div className="mt-4 grid gap-2" aria-busy="true" aria-label={t("certs.checkTitle")}>
          {[0, 1, 2, 3, 4].map((i) => (
            <Skeleton key={i} style={{ height: 56 }} />
          ))}
        </div>
      ) : run.isError ? (
        <div className="mt-4">
          <ErrorState title={t("certs.checkFailed")} text={errorText(run.error)} onRetry={() => run.mutate()} />
        </div>
      ) : report ? (
        <>
          <div className="mt-4 flex items-center gap-2" role="status">
            {fails + warns === 0 ? <Pill tone="ok">{t("certs.allGood")}</Pill> : <Pill tone={fails ? "bad" : "warn"}>{t("certs.summary", { fail: fails, warn: warns })}</Pill>}
          </div>
          <ul className="row-list mt-3">
            {report.checks.map((c, i) => (
              <li key={`${c.id}-${c.node_id ?? 0}-${c.params?.port ?? ""}-${c.params?.inbound ?? ""}-${i}`} className="flex gap-3 py-3">
                <span className="pt-0.5">{ICON[c.status]}</span>
                <div className="min-w-0 flex-1">
                  <div className="text-xs text-[var(--ink-500)]">
                    {tMaybe(`certCheck.title.${c.id}`) ?? c.id}
                    {c.node ? ` · ${t("certs.node", { name: c.node })}` : ""}
                  </div>
                  {c.code === "acme_error" || c.code === "node_acme_error" ? (
                    <AcmeProblem code={c.params?.error ?? ""} detail={c.detail} holder={c.params?.holder} retryAt={c.params?.retry_at} className="mt-0.5" />
                  ) : (
                    <>
                      <p className="mt-0.5 text-[13px] text-[var(--ink-800)]">{checkText(c)}</p>
                      {c.params?.holder ? <p className="mt-1 text-xs text-[var(--ink-600)]">{t("certs.holder", { holder: c.params.holder })}</p> : null}
                      <Details text={c.detail} />
                    </>
                  )}
                  {c.fix ? <div className="mt-2 flex flex-wrap gap-2">{fix(c)}</div> : null}
                </div>
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </Drawer>
  );
}
