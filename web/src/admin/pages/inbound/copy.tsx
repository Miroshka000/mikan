import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError, errorText, unwrap, type Inbound, type Schemas } from "../../../api/client";
import { qk, useNodes } from "../../../api/hooks";
import { Drawer } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Button, Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { destIsIP } from "../../../lib/format";
import { nodeLabel } from "../../../lib/node-label";
import { TargetPicker } from "./target";

/** A copy of a configured protocol: the same template with its keys, on the port and node
 * the form names. Only what the request overrides — port, camouflage site — changes. */
export function CopyDrawer({ inbound, onClose }: { inbound: Inbound | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const nodes = useNodes();
  const [nodeId, setNodeId] = useState(0); // 0: nothing picked yet; else the chosen node
  const [port, setPort] = useState("");
  const [dest, setDest] = useState(""); // the camouflage site when the inbound has one
  const [sni, setSni] = useState(""); // the site name clients send when dest is an IP
  const [errors, setErrors] = useState<Record<string, string>>({});
  useEffect(() => {
    if (!inbound) return;
    setNodeId(inbound.node_id);
    setPort(inbound.port);
    setDest(inbound.dest ?? "");
    setSni(inbound.server_names?.[0] ?? "");
    setErrors({});
    // `nodes` is not reset either: the drawer opens for one inbound at a time.
  }, [inbound]);
  const create = useMutation({
    mutationFn: (body: Schemas["CopyInboundInputBody"]) => unwrap(api.POST("/api/v1/inbounds/{id}/copy", { params: { path: { id: inbound!.id } }, body })),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: qk.inbounds });
      toast.ok(t("inbounds.copiedOn", { name: r.sub_name, port: r.port }));
      onClose();
    },
    onError: (e) => {
      if (e instanceof ApiError && Object.keys(e.fields).length) setErrors(e.fields);
      else toast.error(errorText(e));
    },
  });
  if (!inbound) return null;
  const ip = destIsIP(dest);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    // An empty port is the source's: the server takes its default from there.
    const body: Schemas["CopyInboundInputBody"] = { port: port.trim() || undefined };
    if (nodeId && nodeId !== inbound.node_id) body.node_id = nodeId;
    if (inbound.dest !== undefined && (dest.trim() !== inbound.dest || (ip && sni.trim() !== (inbound.server_names?.[0] ?? "")))) {
      if (ip && !sni.trim()) {
        setErrors({ server_name: t("inbounds.sniRequired") });
        return;
      }
      body.dest = dest.trim();
      if (ip) body.server_name = sni.trim();
    }
    create.mutate(body);
  };
  return (
    <Drawer
      open={!!inbound}
      onOpenChange={(v) => !v && onClose()}
      title={t("inbounds.copyTitle", { name: inbound.sub_name })}
      meta={t("inbounds.copyMeta")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" type="submit" form="copy-inbound" loading={create.isPending}>
            {t("inbounds.copy")}
          </Button>
        </>
      }
    >
      <form id="copy-inbound" onSubmit={submit} className="pt-5" noValidate>
        <p className="banner info mb-4" role="note">
          {t("inbounds.copyWhat", { name: inbound.sub_name })}
        </p>
        {nodes.data && nodes.data.length > 1 ? (
          <Field label={t("inbounds.node")} htmlFor="copy-node">
            <select id="copy-node" className="input max-w-[320px]" value={nodeId} onChange={(e) => setNodeId(Number(e.target.value))}>
              {nodes.data.map((n) => (
                <option key={n.id} value={n.id} disabled={!n.enabled}>
                  {nodeLabel(n)}
                  {n.enabled ? "" : ` — ${t("nodes.disabled")}`}
                </option>
              ))}
            </select>
          </Field>
        ) : null}
        <Field label={t("inbounds.portLabel")} htmlFor="copy-port" error={errors.port} hint={inbound.type === "hysteria2" ? t("inbounds.portHopHint") : undefined}>
          <input
            id="copy-port"
            className="input max-w-[200px]"
            inputMode="numeric"
            value={port}
            onChange={(e) => {
              setPort(e.target.value);
              setErrors(({ port: _, ...rest }) => rest);
            }}
            aria-invalid={!!errors.port}
          />
        </Field>
        {inbound.dest !== undefined ? (
          <>
            <Field
              label={t("inbounds.dest")}
              htmlFor="copy-dest"
              error={errors.dest}
              hint={destIsIP(dest) ? t("inbounds.destHintIP") : t("inbounds.destHint")}
            >
              <input
                id="copy-dest"
                className="input mono"
                value={dest}
                onChange={(e) => {
                  setDest(e.target.value);
                  setErrors(({ dest: _, ...rest }) => rest);
                }}
                placeholder="www.microsoft.com:443"
                aria-invalid={!!errors.dest}
                spellCheck={false}
              />
            </Field>
            {destIsIP(dest) ? (
              <Field label={t("inbounds.sniLabel")} htmlFor="copy-sni" error={errors.server_name} hint={t("inbounds.sniHint")}>
                <input
                  id="copy-sni"
                  className="input mono"
                  value={sni}
                  onChange={(e) => {
                    setSni(e.target.value);
                    setErrors(({ server_name: _, ...rest }) => rest);
                  }}
                  placeholder="example.com"
                  aria-invalid={!!errors.server_name}
                  spellCheck={false}
                  autoComplete="off"
                />
              </Field>
            ) : null}
            <TargetPicker
              dest={dest}
              sni={sni}
              nodeId={nodeId || inbound.node_id}
              onPick={(d, s) => {
                setDest(d);
                setSni(s);
              }}
            />
          </>
        ) : null}
        {inbound.outbound !== "direct" ? <p className="mb-4 text-xs text-[var(--ink-500)]" role="note">{t("inbounds.copyExitNote")}</p> : null}
      </form>
    </Drawer>
  );
}
