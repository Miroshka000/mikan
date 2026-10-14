import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { api, unwrap, type Schemas } from "../../../api/client";
import { onFormError } from "../../../lib/fields";
import { qk, usePresets } from "../../../api/hooks";
import { Drawer, DrawerFooter } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Button, Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { Editor, LimitBadges, LimitNotes, ValidateResult, preloadEditor, presetSummary, presetTitle, useValidate } from "./shared";

export function AddDrawer({ open, onOpenChange, nodeId, nodeName }: { open: boolean; onOpenChange: (v: boolean) => void; nodeId: number; nodeName?: string }) {
  const presets = usePresets();
  const qc = useQueryClient();
  const toast = useToast();
  const validate = useValidate();
  const [preset, setPreset] = useState<string>("vless_reality_xhttp");
  const [port, setPort] = useState("");
  const [config, setConfig] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  useEffect(() => {
    if (open) {
      setPort("");
      setErrors({});
      setConfig(t("inbounds.customSkeleton"));
      validate.reset();
    }
    // `validate` changes identity on every render; reset only when the drawer opens.
  }, [open]);
  const editConfig = (v: string) => {
    setConfig(v);
    setErrors(({ config: _, ...rest }) => rest);
    validate.reset();
  };
  const create = useMutation({
    mutationFn: (body: Schemas["CreateInboundInputBody"]) => unwrap(api.POST("/api/v1/inbounds", { body })),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: qk.inbounds });
      toast.ok(t("inbounds.addedOn", { name: r.sub_name, port: r.port }));
      onOpenChange(false);
    },
    onError: (e) => {
      validate.reset();
      onFormError(e, toast, setErrors);
    },
  });
  const custom = preset === "custom";
  useEffect(() => {
    if (custom && open) preloadEditor();
  }, [custom, open]);
  const chosen = presets.data?.find((p) => p.id === preset);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (custom && !port.trim()) {
      setErrors({ port: t("inbounds.portRequired") });
      return;
    }
    create.mutate({ preset: preset as Schemas["CreateInboundInputBody"]["preset"], node_id: nodeId, port: port.trim() || undefined, config: custom ? config : undefined });
  };
  return (
    <Drawer
      open={open}
      onOpenChange={onOpenChange}
      title={t("inbounds.newTitle")}
      meta={nodeName ? t("inbounds.newOn", { name: nodeName }) : custom ? t("inbounds.customMeta") : t("inbounds.newMeta")}
      footer={
        <DrawerFooter form="add-inbound" onCancel={() => onOpenChange(false)} loading={create.isPending}>
          {t("common.add")}
        </DrawerFooter>
      }
    >
      <form id="add-inbound" onSubmit={submit} className="pt-5" noValidate>
        <Field label={t("inbounds.protocol")}>
          <div className="grid gap-2" role="radiogroup" aria-label={t("inbounds.protocol")}>
            {(presets.data ?? []).map((p) => (
              <button key={p.id} type="button" role="radio" aria-checked={preset === p.id} className="opt" onClick={() => setPreset(p.id)}>
                <span className="font-semibold">{presetTitle(p)}</span>
                <span className="text-xs text-[var(--ink-500)]">{presetSummary(p)}</span>
                <LimitBadges clashOnly={p.apps === "mihomo"} shared={!!p.shared} />
              </button>
            ))}
          </div>
        </Field>
        <Field
          label={t("inbounds.portLabel")}
          htmlFor="in-port"
          hint={custom ? t("inbounds.portHintCustom") : t("inbounds.portHint", { port: chosen?.default_port ?? "—", network: chosen?.network ?? "" })}
          error={errors.port}
        >
          <input id="in-port" className="input max-w-[200px]" inputMode="numeric" placeholder={chosen?.default_port} value={port} onChange={(e) => setPort(e.target.value)} aria-invalid={!!errors.port} />
        </Field>
        {chosen && !custom ? <LimitNotes clashOnly={chosen.apps === "mihomo"} shared={!!chosen.shared} domainCert={!!chosen.domain_cert} /> : null}
        {custom ? (
          <Field label={t("inbounds.configLabel")} hint={t("inbounds.configHint")} error={errors.config}>
            <Editor value={config} onChange={editConfig} invalid={!!errors.config} />
            <div className="mt-2 flex flex-wrap items-center gap-3">
              <Button size="sm" loading={validate.isPending} onClick={() => validate.mutate({ config, node_id: nodeId, port: port.trim() || undefined })}>
                {t("inbounds.validate")}
              </Button>
              <ValidateResult v={validate} />
            </div>
          </Field>
        ) : null}
      </form>
    </Drawer>
  );
}
