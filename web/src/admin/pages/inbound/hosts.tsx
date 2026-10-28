import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, unwrap, type Inbound, type Schemas } from "../../../api/client";
import { qk } from "../../../api/hooks";
import { FingerprintSelect } from "../../../components/fingerprint-select";
import { useToast } from "../../../components/toast";
import { Button, Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { fingerprintLabel } from "../../../lib/fingerprints";
import { onFormError } from "../../../lib/fields";

type Host = Schemas["HostView"];

/** A host as its inputs hold it; id 0 is one not saved yet. */
type HostForm = { id: number; address: string; port: string; sni: string; host: string; fingerprint: string };

const formOf = (h: Host): HostForm => ({ id: h.id, address: h.address, port: h.port ? String(h.port) : "", sni: h.sni, host: h.host, fingerprint: h.fingerprint });
const emptyForm: HostForm = { id: 0, address: "", port: "", sni: "", host: "", fingerprint: "" };

const hostsKey = (id: number) => [...qk.inbounds, id, "hosts"] as const;

/**
 * The hosts of an inbound: the places its clients reach it at. The default one is the
 * inbound's own address for clients (the proxy block above), shown here and changed there;
 * the others are kept in their order for subscriptions to come.
 */
export function HostsSettings({ inbound, defaultFingerprint }: { inbound: Inbound; defaultFingerprint: string }) {
  const hosts = useQuery({
    queryKey: hostsKey(inbound.id),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/inbounds/{id}/hosts", { params: { path: { id: inbound.id } }, signal })),
  });
  const [adding, setAdding] = useState(false);
  const list = hosts.data ?? [];
  const def = list.find((h) => h.default);
  return (
    <div className="space-y-4">
      <p className="text-xs text-[var(--ink-500)]" role="note">
        {t("inbounds.hosts.note")}
      </p>
      {def ? (
        <div className="text-sm">
          <span className="font-semibold">{t("inbounds.hosts.default")}</span>{" "}
          <span className="mono text-[var(--ink-500)]">{describe(def, inbound)}</span>
          <p className="text-xs text-[var(--ink-500)]">{t("inbounds.hosts.defaultHint")}</p>
        </div>
      ) : null}
      {list
        .filter((h) => !h.default)
        .map((h) => (
          <HostRow key={h.id} inbound={inbound} initial={formOf(h)} defaultFingerprint={defaultFingerprint} />
        ))}
      {adding ? <HostRow inbound={inbound} initial={emptyForm} defaultFingerprint={defaultFingerprint} onDone={() => setAdding(false)} /> : null}
      {!adding ? (
        <Button type="button" size="sm" onClick={() => setAdding(true)} disabled={!hosts.data}>
          <Plus size={14} aria-hidden /> {t("inbounds.hosts.add")}
        </Button>
      ) : null}
    </div>
  );
}

/** A host in one line: its address and port, or what clients get without them. */
function describe(h: Host, inbound: Inbound): string {
  const parts = [`${h.address || t("inbounds.hosts.nodeAddress")}:${h.port || inbound.port}`];
  if (h.sni) parts.push(`SNI ${h.sni}`);
  if (h.host) parts.push(`Host ${h.host}`);
  if (h.fingerprint) parts.push(fingerprintLabel(h.fingerprint));
  return parts.join(" · ");
}

/** One host other than the default one, saved (initial.id) or new; onDone closes a new one. */
function HostRow({ inbound, initial, defaultFingerprint, onDone }: { inbound: Inbound; initial: HostForm; defaultFingerprint: string; onDone?: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [f, setF] = useState(initial);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [fpOK, setFpOK] = useState(true);
  const set = (patch: Partial<HostForm>) => {
    setF((v) => ({ ...v, ...patch }));
    setErrors({});
  };
  const uid = `host-${inbound.id}-${initial.id || "new"}`;
  const done = () => {
    void qc.invalidateQueries({ queryKey: hostsKey(inbound.id) });
    onDone?.();
  };
  const save = useMutation({
    mutationFn: (body: Schemas["HostInput"]) =>
      f.id
        ? unwrap(api.PUT("/api/v1/inbounds/{id}/hosts/{host_id}", { params: { path: { id: inbound.id, host_id: f.id } }, body }))
        : unwrap(api.POST("/api/v1/inbounds/{id}/hosts", { params: { path: { id: inbound.id } }, body })),
    onSuccess: () => {
      toast.ok(t("inbounds.hosts.saved"));
      done();
    },
    onError: (e) => onFormError(e, toast, setErrors),
  });
  const remove = useMutation({
    mutationFn: () => unwrap(api.DELETE("/api/v1/inbounds/{id}/hosts/{host_id}", { params: { path: { id: inbound.id, host_id: f.id } } })),
    onSuccess: () => {
      toast.ok(t("inbounds.hosts.deleted"));
      done();
    },
    onError: (e) => onFormError(e, toast),
  });
  const submit = () => {
    const port = f.port.trim() === "" ? 0 : Number(f.port.trim());
    if (!Number.isInteger(port) || port < 0 || port > 65535) {
      setErrors({ port: t("errors.api.config_client_port") });
      return;
    }
    save.mutate({
      address: f.address.trim(),
      port,
      sni: inbound.client_sni ? f.sni.trim() : "",
      host: inbound.client_host ? f.host.trim() : "",
      fingerprint: inbound.fingerprint !== undefined ? f.fingerprint.trim() : "",
    });
  };
  return (
    <div className="space-y-3 rounded-lg border border-[var(--hairline)] p-3">
      <div className="grid gap-3 sm:grid-cols-[1fr_120px]">
        <Field label={t("inbounds.clientServer")} htmlFor={`${uid}-address`} error={errors.address}>
          <input
            id={`${uid}-address`}
            className="input mono"
            value={f.address}
            onChange={(e) => set({ address: e.target.value })}
            placeholder={t("inbounds.clientServerDefault")}
            aria-invalid={!!errors.address}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
        <Field label={t("inbounds.clientPort")} htmlFor={`${uid}-port`} error={errors.port}>
          <input
            id={`${uid}-port`}
            className="input mono"
            inputMode="numeric"
            value={f.port}
            onChange={(e) => set({ port: e.target.value })}
            placeholder={inbound.port}
            aria-invalid={!!errors.port}
            autoComplete="off"
          />
        </Field>
      </div>
      {inbound.client_sni ? (
        <Field label={t("inbounds.clientSni")} htmlFor={`${uid}-sni`} error={errors.sni}>
          <input
            id={`${uid}-sni`}
            className="input mono"
            value={f.sni}
            onChange={(e) => set({ sni: e.target.value })}
            placeholder={t("inbounds.clientSniDefault")}
            aria-invalid={!!errors.sni}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
      ) : null}
      {inbound.client_host ? (
        <Field label={t("inbounds.hosts.host")} htmlFor={`${uid}-host`} error={errors.host}>
          <input
            id={`${uid}-host`}
            className="input mono"
            value={f.host}
            onChange={(e) => set({ host: e.target.value })}
            placeholder={t("inbounds.hosts.hostDefault")}
            aria-invalid={!!errors.host}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
      ) : null}
      {inbound.fingerprint !== undefined ? (
        <Field label={t("inbounds.fingerprint")} htmlFor={`${uid}-fp`} error={errors.fingerprint}>
          <FingerprintSelect
            id={`${uid}-fp`}
            value={f.fingerprint}
            onChange={(fingerprint) => set({ fingerprint })}
            defaultLabel={t("inbounds.fingerprintDefault", { fp: fingerprintLabel(defaultFingerprint) })}
            invalid={!!errors.fingerprint}
            onValid={setFpOK}
          />
        </Field>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" variant="primary" loading={save.isPending} disabled={!fpOK} onClick={submit}>
          {t("common.save")}
        </Button>
        {f.id ? (
          <Button type="button" size="sm" variant="danger" loading={remove.isPending} onClick={() => remove.mutate()}>
            <Trash2 size={14} aria-hidden /> {t("common.delete")}
          </Button>
        ) : (
          <Button type="button" size="sm" variant="ghost" onClick={onDone}>
            {t("common.cancel")}
          </Button>
        )}
      </div>
    </div>
  );
}
