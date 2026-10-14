import { useMutation } from "@tanstack/react-query";
import { CircleCheck, TriangleAlert } from "lucide-react";
import { lazy, Suspense } from "react";
import { api, ApiError, errorText, unwrap, type Preset, type Schemas } from "../../../api/client";
import { Pill, Skeleton } from "../../../components/ui";
import { t, tMaybe } from "../../../i18n";

const loadEditor = () => import("../../../components/config-editor");
const ConfigEditor = lazy(loadEditor);

/** Starts fetching the editor's chunk (CodeMirror, 400 kB) before the config tab is opened, so the tab does not wait for it. */
export function preloadEditor() {
  void loadEditor();
}

/** Protocol names are the same in every language; the one-line pitch is translated. */
export function presetSummary(p: Preset): string {
  return tMaybe(`presets.${p.id}`) ?? p.summary;
}

export function presetTitle(p: Preset): string {
  return p.id === "custom" ? t("inbounds.customTitle") : p.title;
}

export function Editor(props: { value: string; onChange: (v: string) => void; invalid?: boolean }) {
  return (
    <Suspense fallback={<Skeleton style={{ height: 280, borderRadius: 12 }} />}>
      <ConfigEditor {...props} label={t("inbounds.configLabel")} />
    </Suspense>
  );
}

/** Badges of a preset that not every app or feature works with. */
export function LimitBadges({ clashOnly, shared }: { clashOnly: boolean; shared: boolean }) {
  if (!clashOnly && !shared) return null;
  return (
    <span className="mt-1 flex flex-wrap gap-1.5">
      {clashOnly ? <Pill tone="off">{t("inbounds.clashOnly")}</Pill> : null}
      {shared ? <Pill tone="warn">{t("inbounds.sharedKey")}</Pill> : null}
    </span>
  );
}

/** What the admin gives up with the chosen preset, said before it is added. */
export function LimitNotes({ clashOnly, shared, domainCert = false }: { clashOnly: boolean; shared: boolean; domainCert?: boolean }) {
  if (!clashOnly && !shared && !domainCert) return null;
  return (
    <div className="mb-4 flex flex-col gap-2" role="note">
      {shared ? (
        <div className="rounded-2xl border border-[color-mix(in_srgb,var(--honey-500)_35%,transparent)] bg-[var(--honey-50)] p-3 text-[13px] text-[var(--ink-700)]">
          <div className="mb-1 flex items-center gap-2 font-semibold text-[var(--ink-900)]">
            <TriangleAlert size={16} className="text-[var(--honey-600)]" aria-hidden /> {t("inbounds.sharedWarnTitle")}
          </div>
          {t("inbounds.sharedWarn")}
        </div>
      ) : null}
      {clashOnly ? <p className="text-xs text-[var(--ink-500)]">{t("inbounds.clashOnlyNote")}</p> : null}
      {domainCert ? <p className="text-xs text-[var(--ink-500)]">{t("inbounds.domainCertNote")}</p> : null}
    </div>
  );
}

export function listenerError(e: string): string {
  if (e.includes("address already in use")) return t("inbounds.errPortBusy");
  if (e.includes("permission denied")) return t("inbounds.errPermission");
  return e;
}

/** Checks a template on the server (mikan's rules, then mihomo's parser on the node). */
export function useValidate() {
  return useMutation({
    mutationFn: (body: Schemas["ValidateInboundInputBody"]) => unwrap(api.POST("/api/v1/inbounds/validate", { body })),
  });
}

export function ValidateResult({ v }: { v: ReturnType<typeof useValidate> }) {
  if (v.isSuccess) {
    return (
      <span className="inline-flex items-center gap-1.5 text-[13px] text-[var(--leaf-700)]" role="status">
        <CircleCheck size={16} aria-hidden /> {t("inbounds.validOk", { type: v.data.type, network: v.data.network })}
      </span>
    );
  }
  if (v.isError) {
    const msg = v.error instanceof ApiError ? (v.error.fields.config ?? errorText(v.error)) : errorText(v.error);
    return (
      <span className="text-[13px] text-[var(--berry-600)]" role="alert">
        {msg}
      </span>
    );
  }
  return null;
}

export type ListenAt = "all" | "local" | "custom";

export const loopback = (addr: string) => addr === "127.0.0.1" || addr === "::1";

/** Which choice of the listen switch an address is. */
export function listenAt(addr: string): ListenAt {
  return addr === "" ? "all" : loopback(addr) ? "local" : "custom";
}

/** host:port as clients and proxies write it: an IPv6 address in brackets. */
export function hostPort(host: string, port: string): string {
  return host.includes(":") ? `[${host}]:${port}` : `${host}:${port}`;
}
