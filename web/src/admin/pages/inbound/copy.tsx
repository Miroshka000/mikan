import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { api, unwrap, type Inbound, type Schemas } from "../../../api/client";
import { qk, useNodes } from "../../../api/hooks";
import { Drawer, DrawerFooter } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { fieldErrors, onFormError } from "../../../lib/fields";
import { destIsIP } from "../../../lib/format";
import { nodeLabel } from "../../../lib/node-label";
import { TargetPicker } from "./target";
import { Select } from "../../../components/select";

/**
 * Copies a set-up protocol to the same or another node: the same settings with keys of its
 * own; only the port and, for REALITY, the masking site may change.
 */
export function CopyDrawer({ inbound, onClose }: { inbound: Inbound | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const nodes = useNodes();
  const [nodeId, setNodeId] = useState(0);
  const [port, setPort] = useState("");
  const [dest, setDest] = useState("");
  const [sni, setSni] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  useEffect(() => {
    if (!inbound) return;
    setNodeId(inbound.node_id);
    setPort("");
    setDest(inbound.dest ?? "");
    setSni(inbound.server_names?.[0] ?? "");
    setErrors({});
  }, [inbound]);
  const copy = useMutation({
    mutationFn: (body: Schemas["CopyInboundInputBody"]) => unwrap(api.POST("/api/v1/inbounds/{id}/copy", { params: { path: { id: inbound!.id } }, body })),
    onSuccess: (v) => {
      void qc.invalidateQueries({ queryKey: qk.inbounds });
      toast.ok(t("inbounds.copy.done", { name: v.sub_name, port: v.port }));
      onClose();
    },
    onError: (e) => {
      if (onFormError(e, toast, setErrors)) setErrors(fieldErrors(e));
    },
  });
  const reality = inbound?.dest !== undefined;
  const sameNode = nodeId === inbound?.node_id;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!inbound) return;
    const body: Schemas["CopyInboundInputBody"] = {};
    if (!sameNode) body.node_id = nodeId;
    if (port.trim()) body.port = port.trim();
    if (reality && dest.trim() && (dest.trim() !== inbound.dest || sni.trim() !== (inbound.server_names?.[0] ?? ""))) {
      if (destIsIP(dest) && !sni.trim()) {
        setErrors({ server_name: t("inbounds.sniRequired") });
        return;
      }
      body.dest = dest.trim();
      if (destIsIP(dest)) body.server_name = sni.trim();
    }
    copy.mutate(body);
  };
  const others = (nodes.data ?? []).length > 1;
  return (
    <Drawer
      open={!!inbound}
      onOpenChange={(v) => !v && onClose()}
      title={t("inbounds.copy.title", { name: inbound?.sub_name ?? "" })}
      meta={inbound ? `${inbound.title} · ${inbound.name}` : undefined}
      footer={
        <DrawerFooter form="copy-inbound" onCancel={onClose} loading={copy.isPending}>
          {t("inbounds.copy.submit")}
        </DrawerFooter>
      }
    >
      <form id="copy-inbound" onSubmit={submit} className="pt-5" noValidate>
        <p className="banner info mb-4" role="note">
          {t("inbounds.copy.lead")}
        </p>
        {others ? (
          <Field label={t("inbounds.copy.node")} htmlFor="cp-node" error={errors.node_id}>
            <Select id="cp-node" className="input max-w-[320px]" value={nodeId} onChange={(e) => setNodeId(Number(e.target.value))}>
              {(nodes.data ?? []).map((n) => (
                <option key={n.id} value={n.id} disabled={!n.enabled}>
                  {nodeLabel(n)}
                  {n.enabled ? "" : ` — ${t("nodes.disabled")}`}
                </option>
              ))}
            </Select>
          </Field>
        ) : null}
        <Field label={t("inbounds.portLabel")} htmlFor="cp-port" hint={t("inbounds.copy.portHint", { port: inbound?.port ?? "" })} error={errors.port}>
          <input
            id="cp-port"
            className="input max-w-[200px]"
            inputMode="numeric"
            value={port}
            onChange={(e) => {
              setPort(e.target.value);
              setErrors(({ port: _, ...rest }) => rest);
            }}
            placeholder={inbound?.port}
            aria-invalid={!!errors.port}
          />
        </Field>
        {reality && inbound ? (
          <>
            <Field label={t("inbounds.dest")} htmlFor="cp-dest" hint={t("inbounds.copy.destHint")} error={errors.dest}>
              <input id="cp-dest" className="input mono" value={dest} onChange={(e) => setDest(e.target.value)} placeholder="www.microsoft.com:443" aria-invalid={!!errors.dest} spellCheck={false} />
            </Field>
            {destIsIP(dest) ? (
              <Field label={t("inbounds.sniLabel")} htmlFor="cp-sni" error={errors.server_name} hint={t("inbounds.sniHint")}>
                <input id="cp-sni" className="input mono" value={sni} onChange={(e) => setSni(e.target.value)} placeholder="example.com" spellCheck={false} autoComplete="off" />
              </Field>
            ) : null}
            <TargetPicker
              dest={dest}
              sni={sni}
              nodeId={nodeId}
              onPick={(d, s) => {
                setDest(d);
                setSni(s);
              }}
            />
          </>
        ) : null}
        <p className="text-xs text-[var(--ink-500)]">{sameNode ? t("inbounds.copy.noteSame") : t("inbounds.copy.noteOther")}</p>
      </form>
    </Drawer>
  );
}
