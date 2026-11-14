import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, unwrap, type Inbound, type Schemas } from "../../../api/client";
import { qk } from "../../../api/hooks";
import { FingerprintSelect } from "../../../components/fingerprint-select";
import { Confirm } from "../../../components/overlay";
import { Select } from "../../../components/select";
import { SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { fingerprintLabel } from "../../../lib/fingerprints";
import { onFormError } from "../../../lib/fields";

type Host = Schemas["HostView"];

/** A host's own fields as its inputs hold them: those no template holds, the default host's too. */
type OwnForm = { alpn: string[]; ipVersion: string; mux: boolean; muxProtocol: string; muxPadding: boolean; cdn: boolean };

/** A host as its inputs hold it; id 0 is one not saved yet. */
type HostForm = OwnForm & { id: number; address: string; port: string; sni: string; host: string; fingerprint: string };

const formOf = (h: Host): HostForm => ({
  id: h.id,
  address: h.address,
  port: h.port ? String(h.port) : "",
  sni: h.sni,
  host: h.host,
  fingerprint: h.fingerprint,
  alpn: h.alpn,
  ipVersion: h.ip_version,
  mux: h.mux.enabled,
  muxProtocol: h.mux.protocol,
  muxPadding: h.mux.padding,
  cdn: h.cdn,
});
const emptyForm: HostForm = { id: 0, address: "", port: "", sni: "", host: "", fingerprint: "", alpn: [], ipVersion: "", mux: false, muxProtocol: "", muxPadding: false, cdn: false };

const IP_VERSIONS = ["ipv4", "ipv6", "ipv4-prefer", "ipv6-prefer"] as const;
const MUX_PROTOCOLS = ["smux", "yamux", "h2mux"];

type OwnFieldsCan = { alpn: boolean; alpnValues: string[]; mux: boolean; cdn: boolean };

/** Which own fields the inbound's protocol can use; the panel refuses the others with a reason. */
function ownFieldsOf(inbound: Inbound): OwnFieldsCan {
  return {
    // TLS on the node's certificate, or QUIC: a REALITY client speaks the target site's TLS.
    alpn: !!inbound.node_cert || inbound.network === "udp",
    // h3 is QUIC's alone; TLS over TCP offers h2 and HTTP/1.1.
    alpnValues: inbound.network === "udp" ? ["h3"] : ["h2", "http/1.1"],
    // sing-mux on VLESS, Trojan and Shadowsocks; XHTTP multiplexes its own requests.
    mux: ["vless", "trojan", "shadowsocks"].includes(inbound.type) && !inbound.xhttp,
    cdn: inbound.cdn_possible,
  };
}

/** The own fields of a form as the API takes them. */
function ownBody(f: OwnForm, can: OwnFieldsCan): Pick<Schemas["HostInput"], "alpn" | "ip_version" | "mux" | "cdn"> {
  return {
    alpn: can.alpn ? can.alpnValues.filter((a) => f.alpn.includes(a)) : [],
    ip_version: f.ipVersion,
    mux: can.mux && f.mux ? { enabled: true, protocol: f.muxProtocol, padding: f.muxPadding } : { enabled: false, protocol: "", padding: false },
    cdn: can.cdn && f.cdn,
  };
}

const hostsKey = (id: number) => [...qk.inbounds, id, "hosts"] as const;

/**
 * The hosts of an inbound: the places its clients reach it at. The default one is the
 * inbound's own address for clients (the proxy block above), shown here and changed there;
 * each other one is one more server in the subscriptions, in their order.
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
      {def ? <DefaultHostRow key={def.id} inbound={inbound} host={def} /> : null}
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

/**
 * The default host: its address and the rest are the inbound's own for clients, changed in
 * the proxy block above; its own fields are changed here. A save sends the five others as
 * the list has them now, so that a change made above in the meantime stays.
 */
function DefaultHostRow({ inbound, host }: { inbound: Inbound; host: Host }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [f, setF] = useState<OwnForm>(() => formOf(host));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const can = ownFieldsOf(inbound);
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT("/api/v1/inbounds/{id}/hosts/{host_id}", {
          params: { path: { id: inbound.id, host_id: host.id } },
          body: { address: host.address, port: host.port, sni: host.sni, host: host.host, fingerprint: host.fingerprint, ...ownBody(f, can) },
        }),
      ),
    onSuccess: () => {
      toast.ok(t("inbounds.hosts.saved"));
      void qc.invalidateQueries({ queryKey: qk.inbounds });
    },
    onError: (e) => onFormError(e, toast, setErrors),
  });
  return (
    <div className="space-y-3 rounded-lg border border-[var(--hairline)] p-3">
      <div className="text-sm">
        <span className="font-semibold">{t("inbounds.hosts.default")}</span> <span className="mono text-[var(--ink-500)]">{describe(host, inbound)}</span>
        <p className="text-xs text-[var(--ink-500)]">{t("inbounds.hosts.defaultHint")}</p>
      </div>
      <OwnFields
        uid={`host-${inbound.id}-default`}
        f={f}
        set={(patch) => {
          setF((v) => ({ ...v, ...patch }));
          setErrors({});
        }}
        can={can}
        errors={errors}
      />
      <Button type="button" size="sm" variant="primary" loading={save.isPending} onClick={() => save.mutate()}>
        {t("common.save")}
      </Button>
    </div>
  );
}

/** The inputs of a host's own fields; those the protocol cannot use are not shown. */
function OwnFields({ uid, f, set, can, errors }: { uid: string; f: OwnForm; set: (patch: Partial<OwnForm>) => void; can: OwnFieldsCan; errors: Record<string, string> }) {
  return (
    <>
      {can.alpn ? (
        <Field label={t("inbounds.hosts.alpn")} hint={t("inbounds.hosts.alpnHint")} error={errors.alpn}>
          <div className="flex flex-wrap gap-4">
            {can.alpnValues.map((a) => (
              <label key={a} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  className="check shrink-0"
                  checked={f.alpn.includes(a)}
                  onChange={(e) => set({ alpn: e.target.checked ? [...f.alpn, a] : f.alpn.filter((x) => x !== a) })}
                />
                <span className="mono">{a}</span>
              </label>
            ))}
          </div>
        </Field>
      ) : null}
      <Field label={t("inbounds.hosts.ipVersion")} htmlFor={`${uid}-ipv`} error={errors.ip_version}>
        <Select id={`${uid}-ipv`} className="input max-w-[320px]" value={f.ipVersion} onChange={(e) => set({ ipVersion: e.target.value })}>
          <option value="">{t("inbounds.hosts.ipVersionDefault")}</option>
          {IP_VERSIONS.map((v) => (
            <option key={v} value={v}>
              {t(`inbounds.hosts.ipVersions.${v}`)}
            </option>
          ))}
        </Select>
      </Field>
      {can.mux ? (
        <div>
          <SwitchRow label={t("inbounds.hosts.mux")} sub={errors.mux ?? t("inbounds.hosts.muxSub")} warn={!!errors.mux} checked={f.mux} onChange={(mux) => set({ mux })} />
          {f.mux ? (
            <div className="mt-3 space-y-3">
              <Field label={t("inbounds.hosts.muxProtocol")} htmlFor={`${uid}-muxp`} error={errors["mux.protocol"]}>
                <Select id={`${uid}-muxp`} className="input max-w-[320px]" value={f.muxProtocol} onChange={(e) => set({ muxProtocol: e.target.value })}>
                  <option value="">{t("inbounds.hosts.muxProtocolDefault")}</option>
                  {MUX_PROTOCOLS.map((p) => (
                    <option key={p} value={p}>
                      {p}
                    </option>
                  ))}
                </Select>
              </Field>
              <SwitchRow label={t("inbounds.hosts.muxPadding")} sub={t("inbounds.hosts.muxPaddingSub")} checked={f.muxPadding} onChange={(muxPadding) => set({ muxPadding })} />
            </div>
          ) : null}
        </div>
      ) : null}
      {can.cdn ? (
        <SwitchRow label={t("inbounds.hosts.cdn")} sub={errors.cdn ?? t("inbounds.hosts.cdnSub")} warn={!!errors.cdn} checked={f.cdn} onChange={(cdn) => set({ cdn })} />
      ) : null}
    </>
  );
}

/** One host other than the default one, saved (initial.id) or new; onDone closes a new one. */
function HostRow({ inbound, initial, defaultFingerprint, onDone }: { inbound: Inbound; initial: HostForm; defaultFingerprint: string; onDone?: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [f, setF] = useState(initial);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [fpOK, setFpOK] = useState(true);
  const [confirming, setConfirming] = useState(false);
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
      setConfirming(false);
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
      ...ownBody(f, ownFieldsOf(inbound)),
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
      <OwnFields uid={uid} f={f} set={set} can={ownFieldsOf(inbound)} errors={errors} />
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" variant="primary" loading={save.isPending} disabled={!fpOK} onClick={submit}>
          {t("common.save")}
        </Button>
        {f.id ? (
          <Button type="button" size="sm" variant="danger" onClick={() => setConfirming(true)}>
            <Trash2 size={14} aria-hidden /> {t("common.delete")}
          </Button>
        ) : (
          <Button type="button" size="sm" variant="ghost" onClick={onDone}>
            {t("common.cancel")}
          </Button>
        )}
      </div>
      <Confirm
        open={confirming}
        onOpenChange={setConfirming}
        title={t("inbounds.hosts.deleteTitle", { address: f.address.trim() || t("inbounds.hosts.nodeAddress") })}
        text={t("inbounds.hosts.deleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => remove.mutate()}
      />
    </div>
  );
}
