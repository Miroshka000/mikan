import { useMutation, useQueryClient } from "@tanstack/react-query";
import { RotateCcw } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { api, unwrap, type Inbound, type Schemas } from "../../../api/client";
import { useFields } from "../../../lib/draft";
import { fieldErrors, onFormError } from "../../../lib/fields";
import { qk, useNodes, usePools, usePresets, useSettings } from "../../../api/hooks";
import { Disclosure } from "../../../components/layout";
import { Drawer, DrawerFooter } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Button, Field, Segmented } from "../../../components/ui";
import { XHTTP_DEFAULT, XhttpTuning, type XHTTP } from "./xhttp";
import { SwitchRow } from "../../../components/switch";
import { t } from "../../../i18n";
import { FingerprintSelect } from "../../../components/fingerprint-select";
import { fingerprintLabel, validFingerprint } from "../../../lib/fingerprints";
import { destIsIP } from "../../../lib/format";
import { nodeLabel } from "../../../lib/node-label";
import { useWarp } from "../node-warp";
import { Editor, type ListenAt, ValidateResult, hostPort, listenAt, loopback, preloadEditor, useValidate } from "./shared";
import { TargetPicker } from "./target";
import { CdnSettings, cdnClient, cdnFormOf, type CdnForm } from "./cdn";
import { Select } from "../../../components/select";

/** The inbound form's fields as its inputs hold them. */
type InboundForm = {
  port: string;
  dest: string;
  sni: string; // the site name clients send when dest is an IP
  fp: string; // the inbound's own fingerprint, "" for the settings' one
  obfs: string; // Hysteria2: salamander or gecko
  xhttp: XHTTP; // the masking tab, XHTTP only
  outbound: Inbound["outbound"];
  exitNode: number;
  poolId: number;
  name: string;
  config: string;
  autoPort: boolean;
  autoSni: boolean;
  listenMode: ListenAt;
  listenIP: string; // the address of its own for "custom"
  clientServer: string; // what clients get: "" = as now
  clientPort: string;
  clientSni: string;
  cdn: CdnForm | null;
  sni443: boolean;
};

/** The form of inbound i; with none, an empty one. The one place that maps an inbound to the form. */
function inboundFormOf(i: Inbound | null): InboundForm {
  return {
    port: i?.port ?? "",
    dest: i?.dest ?? "",
    sni: i?.server_names?.[0] ?? "",
    fp: i?.fingerprint ?? "",
    obfs: i?.obfs ?? "",
    xhttp: { ...XHTTP_DEFAULT, ...i?.xhttp },
    outbound: i?.outbound ?? "direct",
    exitNode: i?.exit_node_id ?? 0,
    poolId: i?.pool_id ?? 0,
    name: i?.display_name ?? "",
    config: i?.config ?? "",
    autoPort: i?.auto_port ?? true,
    autoSni: i?.auto_sni ?? true,
    listenMode: i ? listenAt(i.listen) : "all",
    listenIP: i && listenAt(i.listen) === "custom" ? i.listen : "",
    clientServer: i?.client.server ?? "",
    clientPort: i?.client.port ? String(i.client.port) : "",
    clientSni: i?.client.sni ?? "",
    cdn: i ? cdnFormOf(i) : null,
    sni443: i?.sni443 ?? false,
  };
}

export function EditDrawer({ inbound, onClose }: { inbound: Inbound | null; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const presets = usePresets();
  const validate = useValidate();
  const { reset: resetValidate } = validate;
  const [tab, setTab] = useState<"main" | "xhttp" | "config">("main");
  const [form, set, replace] = useFields(() => inboundFormOf(null));
  const { port, dest, sni, fp, obfs, xhttp, outbound, exitNode, poolId, name, config, autoPort, autoSni, listenMode, listenIP, clientServer, clientPort, clientSni, cdn, sni443 } = form;
  const pools = usePools();
  const allNodes = useNodes();
  const [errors, setErrors] = useState<Record<string, string>>({});
  const settings = useSettings();
  const warp = useWarp(inbound?.node_id ?? null, !!inbound);
  const nodeRouter = !!allNodes.data?.find((n) => n.id === inbound?.node_id)?.sni_router;
  useEffect(() => {
    if (!inbound) return;
    setTab("main");
    replace(inboundFormOf(inbound));
    setErrors({});
    resetValidate();
  }, [inbound, replace, resetValidate]);
  // The config tab is a click away: have its editor on the way once the drawer has settled.
  useEffect(() => {
    if (!inbound) return;
    const id = window.setTimeout(preloadEditor, 400);
    return () => window.clearTimeout(id);
  }, [inbound]);
  const defaultName = presets.data?.find((p) => p.id === inbound?.preset)?.sub_name ?? "";
  const editConfig = (v: string) => {
    set.config(v);
    setErrors(({ config: _, ...rest }) => rest);
    validate.reset();
  };
  const save = useMutation({
    mutationFn: (body: Schemas["PatchInboundInputBody"]) => unwrap(api.PATCH("/api/v1/inbounds/{id}", { params: { path: { id: inbound!.id } }, body })),
    onSuccess: (_, body) => {
      void qc.invalidateQueries({ queryKey: qk.inbounds });
      // The automatic-fix switches and the listen address change nothing clients get.
      const autoOnly = Object.keys(body).every((k) => k === "auto_port" || k === "auto_sni" || k === "outbound" || k === "exit_node_id" || k === "pool_id" || k === "listen");
      toast.ok(autoOnly ? t("inbounds.savedAuto") : t("inbounds.saved"));
      onClose();
    },
    onError: (e) => {
      validate.reset();
      if (onFormError(e, toast, setErrors)) {
        const fields = fieldErrors(e);
        setTab(fields.config ? "config" : fields.xhttp ? "xhttp" : "main");
      }
    },
  });
  const configChanged = !!inbound && config !== inbound.config;
  // Localhost keeps a loopback address it has (::1), else takes 127.0.0.1.
  const listen = listenMode === "all" ? "" : listenMode === "local" ? (inbound && loopback(inbound.listen) ? inbound.listen : "127.0.0.1") : listenIP.trim();
  // Behind a proxy the port is what the proxy forwards to: it never moves on its own.
  const behindProxy = listenMode !== "all";
  const clearError = (key: string) => setErrors(({ [key]: _, ...rest }) => rest);
  // These fields live on the first tab: show it, or Save on the config tab does nothing visible.
  const reject = (errs: Record<string, string>) => {
    setErrors(errs);
    setTab("main");
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const body: Schemas["PatchInboundInputBody"] = {};
    if (port !== inbound?.port) body.port = port.trim();
    if (name.trim() !== inbound?.display_name) body.display_name = name.trim();
    // The config tab rewrites the whole template; the dest field is a shortcut into it.
    if (configChanged) body.config = config;
    else if (inbound?.dest !== undefined) {
      const ip = destIsIP(dest);
      if (dest.trim() !== inbound.dest || (ip && sni.trim() !== (inbound.server_names?.[0] ?? ""))) {
        if (ip && !sni.trim()) {
          reject({ server_name: t("inbounds.sniRequired") });
          return;
        }
        body.dest = dest.trim();
        if (ip) body.server_name = sni.trim();
      }
    }
    if (fp !== "" && !validFingerprint(fp)) {
      reject({ fingerprint: t("settings.fpOwnBad") });
      return;
    }
    if (!configChanged && inbound?.fingerprint !== undefined && fp !== inbound.fingerprint) body.fingerprint = fp;
    if (!configChanged && inbound?.obfs !== undefined && obfs !== inbound.obfs && (obfs === "salamander" || obfs === "gecko")) body.obfs = obfs;
    // The whole tuning goes: an emptied field is a value too (back to the default).
    if (!configChanged && inbound?.xhttp && JSON.stringify(xhttp) !== JSON.stringify(inbound.xhttp)) body.xhttp = xhttp;
    if (!configChanged && inbound && sni443 !== inbound.sni443) body.sni443 = sni443;
    if (listenMode === "custom" && !listen) {
      reject({ listen: t("inbounds.listenRequired") });
      return;
    }
    if (listen !== inbound?.listen) body.listen = listen;
    const cport = clientPort.trim() === "" ? 0 : Number(clientPort.trim());
    if (!Number.isInteger(cport) || cport < 0 || cport > 65535) {
      reject({ "client.port": t("errors.api.config_client_port") });
      return;
    }
    // Without a TLS name of its own to change (REALITY, no TLS) the SNI is not the form's.
    let client: Schemas["ClientEndpoint"] = { server: clientServer.trim(), port: cport, sni: inbound?.client_sni ? clientSni.trim() : "", host: "" };
    if (inbound && cdn?.on) {
      const made = cdnClient(cdn, inbound);
      if ("errors" in made) {
        reject(made.errors);
        return;
      }
      client = made.client;
    }
    const was = inbound?.client;
    const clientChanged =
      !!was &&
      (client.server !== was.server ||
        client.port !== was.port ||
        (inbound.client_sni && client.sni !== was.sni) ||
        (client.host ?? "") !== (was.host ?? "") ||
        JSON.stringify(client.cdn ?? null) !== JSON.stringify(was.cdn ?? null));
    if (!configChanged && clientChanged) body.client = client;
    if (poolId !== (inbound?.pool_id ?? 0)) body.pool_id = poolId;
    const autoPortNow = autoPort && !behindProxy;
    if (autoPortNow !== inbound?.auto_port) body.auto_port = autoPortNow;
    const autoSniNow = autoSni && !behindProxy;
    if (autoSniNow !== inbound?.auto_sni) body.auto_sni = autoSniNow;
    if (outbound === "node" && !exitNode) {
      reject({ exit_node_id: t("inbounds.exitPick") });
      return;
    }
    if (outbound !== inbound?.outbound || (outbound === "node" && exitNode !== inbound?.exit_node_id)) {
      body.outbound = outbound;
      if (outbound === "node") body.exit_node_id = exitNode;
    }
    if (Object.keys(body).length === 0) {
      onClose();
      return;
    }
    save.mutate(body);
  };
  return (
    <Drawer
      open={!!inbound}
      onOpenChange={(v) => !v && onClose()}
      title={inbound?.sub_name ?? ""}
      meta={
        inbound ? (
          <span>
            {inbound.preset === "custom" ? t("inbounds.customOf", { type: inbound.type }) : inbound.title} · <span className="mono">{inbound.name}</span>
          </span>
        ) : undefined
      }
      footer={
        <DrawerFooter form="edit-inbound" onCancel={onClose} loading={save.isPending}>
          {t("common.save")}
        </DrawerFooter>
      }
    >
      <form id="edit-inbound" onSubmit={submit} className="pt-5" noValidate>
        <Segmented
          block
          label={t("inbounds.tabs")}
          value={tab}
          onChange={setTab}
          options={[
            { value: "main", label: t("inbounds.tabMain") },
            ...(inbound?.xhttp ? [{ value: "xhttp" as const, label: t("inbounds.xhttp.tab") }] : []),
            { value: "config", label: t("inbounds.tabConfig") },
          ]}
        />
        <div className="mt-4">
          {tab === "main" ? (
            <>
              <p className="banner info mb-4" role="note">
                {t("inbounds.reconnectWarning")}
              </p>
              <Field label={t("inbounds.subName")} htmlFor="ed-name" hint={t("inbounds.subNameHint")} error={errors.display_name}>
                <input id="ed-name" className="input" value={name} onChange={(e) => set.name(e.target.value)} placeholder={defaultName} maxLength={48} aria-invalid={!!errors.display_name} autoComplete="off" />
              </Field>
              <Field label={t("inbounds.portLabel")} htmlFor="ed-port" error={errors.port} hint={inbound?.type === "hysteria2" ? t("inbounds.portHopHint") : undefined}>
                <input id="ed-port" className="input max-w-[200px]" inputMode="numeric" value={port} onChange={(e) => set.port(e.target.value)} aria-invalid={!!errors.port} />
              </Field>
              {inbound?.dest !== undefined ? (
                <>
                  <Field
                    label={t("inbounds.dest")}
                    htmlFor="ed-dest"
                    error={errors.dest}
                    hint={configChanged ? t("inbounds.destLocked") : destIsIP(dest) ? t("inbounds.destHintIP") : t("inbounds.destHint")}
                  >
                    <input
                      id="ed-dest"
                      className="input mono"
                      value={dest}
                      onChange={(e) => set.dest(e.target.value)}
                      placeholder="www.microsoft.com:443"
                      aria-invalid={!!errors.dest}
                      spellCheck={false}
                      disabled={configChanged}
                    />
                  </Field>
                  {destIsIP(dest) ? (
                    <Field label={t("inbounds.sniLabel")} htmlFor="ed-sni" error={errors.server_name} hint={t("inbounds.sniHint")}>
                      <input
                        id="ed-sni"
                        className="input mono"
                        value={sni}
                        onChange={(e) => {
                          set.sni(e.target.value);
                          setErrors(({ server_name: _, ...rest }) => rest);
                        }}
                        placeholder="example.com"
                        aria-invalid={!!errors.server_name}
                        spellCheck={false}
                        autoComplete="off"
                        disabled={configChanged}
                      />
                    </Field>
                  ) : null}
                  {!configChanged ? (
                    <TargetPicker
                      dest={dest}
                      sni={sni}
                      nodeId={inbound.node_id}
                      onPick={(d, s) => {
                        set.dest(d);
                        set.sni(s);
                      }}
                    />
                  ) : null}
                </>
              ) : null}
              {inbound?.obfs !== undefined ? (
                <Field label={t("inbounds.obfs")} error={errors.obfs} hint={configChanged ? t("inbounds.fingerprintLocked") : obfs === "gecko" ? t("inbounds.obfsGeckoHint") : t("inbounds.obfsHint")}>
                  <Segmented
                    value={obfs}
                    label={t("inbounds.obfs")}
                    onChange={(v) => {
                      if (configChanged) return;
                      set.obfs(v);
                      setErrors(({ obfs: _, ...rest }) => rest);
                    }}
                    options={[
                      { value: "salamander", label: "Salamander" },
                      { value: "gecko", label: "Gecko" },
                    ]}
                  />
                </Field>
              ) : null}
              {inbound?.fingerprint !== undefined ? (
                <Field label={t("inbounds.fingerprint")} htmlFor="ed-fp" error={errors.fingerprint} hint={configChanged ? t("inbounds.fingerprintLocked") : t("inbounds.fingerprintHint")}>
                  <FingerprintSelect
                    key={inbound?.id}
                    id="ed-fp"
                    value={fp}
                    onChange={(v) => {
                      set.fp(v);
                      setErrors(({ fingerprint: _, ...rest }) => rest);
                    }}
                    defaultLabel={t("inbounds.fingerprintDefault", { fp: fingerprintLabel(settings.data?.client_fingerprint ?? "chrome") })}
                    invalid={!!errors.fingerprint}
                    disabled={configChanged}
                  />
                </Field>
              ) : null}
              <Field
                label={t("inbounds.outbound")}
                error={errors.exit_node_id}
                hint={
                  outbound === "node"
                    ? t("inbounds.outboundNodeHint")
                    : outbound === "warp" && warp.data && (!warp.data.configured || !warp.data.enabled)
                      ? t("inbounds.outboundNoWarp")
                      : outbound === "warp"
                        ? t("inbounds.outboundWarpHint")
                        : t("inbounds.outboundDirectHint")
                }
              >
                <Segmented
                  label={t("inbounds.outbound")}
                  value={outbound}
                  onChange={(v) => {
                    set.outbound(v);
                    setErrors(({ exit_node_id: _, ...rest }) => rest);
                  }}
                  options={[
                    { value: "direct", label: t("inbounds.outboundDirect") },
                    { value: "warp", label: "WARP" },
                    { value: "node", label: t("inbounds.outboundNode") },
                  ]}
                />
                {outbound === "node" ? (
                  <Select
                    className="input mt-2 max-w-[320px]"
                    value={exitNode}
                    onChange={(e) => {
                      set.exitNode(Number(e.target.value));
                      setErrors(({ exit_node_id: _, ...rest }) => rest);
                    }}
                    aria-label={t("inbounds.exitNode")}
                    aria-invalid={!!errors.exit_node_id}
                  >
                    <option value={0} disabled>
                      {t("inbounds.exitPick")}
                    </option>
                    {(allNodes.data ?? [])
                      .filter((n) => n.id !== inbound?.node_id)
                      .map((n) => (
                        <option key={n.id} value={n.id} disabled={!n.enabled}>
                          {nodeLabel(n)}
                          {n.enabled ? "" : ` — ${t("nodes.disabled")}`}
                        </option>
                      ))}
                  </Select>
                ) : null}
              </Field>
              {pools.data?.length ? (
                <Field label={t("pools.inbound")} htmlFor="ed-pool" hint={t("pools.inboundHint")}>
                  <Select id="ed-pool" className="input max-w-[320px]" value={poolId} onChange={(e) => set.poolId(Number(e.target.value))}>
                    <option value={0}>{t("pools.mainServers")}</option>
                    {pools.data.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </Select>
                </Field>
              ) : null}
              {inbound?.sni443_possible ? (
                <SwitchRow
                  label={t("inbounds.sni443.title")}
                  sub={sni443 && !nodeRouter ? t("inbounds.sni443.routerOff") : t("inbounds.sni443.sub")}
                  warn={sni443 && !nodeRouter}
                  checked={sni443}
                  disabled={configChanged}
                  onChange={set.sni443}
                />
              ) : null}
              {inbound?.cdn_possible && cdn ? (
                <Disclosure title={t("inbounds.cdn.title")} sub={t("inbounds.cdn.sub")} open={cdn.on || Object.keys(errors).some((k) => k.startsWith("client.cdn"))}>
                  <CdnSettings value={cdn} onChange={set.cdn} errors={errors} clearError={clearError} inbound={inbound} disabled={configChanged} />
                </Disclosure>
              ) : null}
              <Disclosure
                title={t("inbounds.proxy")}
                sub={t("common.forExperts")}
                open={!!(errors.listen || (!cdn?.on && (errors["client.server"] || errors["client.port"] || errors["client.sni"]))) || behindProxy}
              >
                <p className="mb-4 text-xs text-[var(--ink-500)]">{t("inbounds.proxySub")}</p>
                <Field
                  label={t("inbounds.listen")}
                  htmlFor={listenMode === "custom" ? "ed-listen" : undefined}
                  error={errors.listen}
                  hint={
                    listenMode === "all"
                      ? t("inbounds.listenAllHint")
                      : listenMode === "local"
                        ? t("inbounds.listenLocalHint", { addr: hostPort(listen, port.trim() || (inbound?.port ?? "")) })
                        : t("inbounds.listenCustomHint")
                  }
                >
                  <Segmented
                    label={t("inbounds.listen")}
                    value={listenMode}
                    onChange={(v) => {
                      set.listenMode(v);
                      clearError("listen");
                    }}
                    options={[
                      { value: "all", label: t("inbounds.listenAll") },
                      { value: "local", label: t("inbounds.listenLocal") },
                      { value: "custom", label: t("inbounds.listenCustom") },
                    ]}
                  />
                  {listenMode === "custom" ? (
                    <input
                      id="ed-listen"
                      className="input mono mt-2 max-w-[320px]"
                      value={listenIP}
                      onChange={(e) => {
                        set.listenIP(e.target.value);
                        clearError("listen");
                      }}
                      placeholder="10.0.0.5"
                      aria-invalid={!!errors.listen}
                      spellCheck={false}
                      autoComplete="off"
                    />
                  ) : null}
                </Field>
                {cdn?.on ? (
                  <p className="text-xs text-[var(--ink-500)]" role="note">
                    {t("inbounds.cdn.proxyNote")}
                  </p>
                ) : (
                <>
                <div className="grid gap-x-4 sm:grid-cols-[1fr_160px]">
                  <Field label={t("inbounds.clientServer")} htmlFor="ed-client-server" error={errors["client.server"]}>
                    <input
                      id="ed-client-server"
                      className="input mono"
                      value={clientServer}
                      onChange={(e) => {
                        set.clientServer(e.target.value);
                        clearError("client.server");
                      }}
                      placeholder={t("inbounds.clientServerDefault")}
                      aria-invalid={!!errors["client.server"]}
                      spellCheck={false}
                      autoComplete="off"
                      disabled={configChanged}
                    />
                  </Field>
                  <Field label={t("inbounds.clientPort")} htmlFor="ed-client-port" error={errors["client.port"]}>
                    <input
                      id="ed-client-port"
                      className="input"
                      inputMode="numeric"
                      value={clientPort}
                      onChange={(e) => {
                        set.clientPort(e.target.value);
                        clearError("client.port");
                      }}
                      placeholder={inbound?.port}
                      aria-invalid={!!errors["client.port"]}
                      disabled={configChanged}
                    />
                  </Field>
                </div>
                {inbound?.client_sni ? (
                  <Field label={t("inbounds.clientSni")} htmlFor="ed-client-sni" error={errors["client.sni"]}>
                    <input
                      id="ed-client-sni"
                      className="input mono"
                      value={clientSni}
                      onChange={(e) => {
                        set.clientSni(e.target.value);
                        clearError("client.sni");
                      }}
                      placeholder={t("inbounds.clientSniDefault")}
                      aria-invalid={!!errors["client.sni"]}
                      spellCheck={false}
                      autoComplete="off"
                      disabled={configChanged}
                    />
                  </Field>
                ) : null}
                <p className={behindProxy && !clientPort.trim() && !configChanged ? "text-xs text-[var(--honey-600)]" : "text-xs text-[var(--ink-500)]"} role="note">
                  {configChanged
                    ? t("inbounds.clientLocked")
                    : behindProxy && !clientPort.trim()
                      ? t("inbounds.clientPortWarn", { port: port.trim() || (inbound?.port ?? "") })
                      : inbound?.dest !== undefined
                        ? t("inbounds.clientHintReality")
                        : t("inbounds.clientHint")}
                </p>
                </>
                )}
              </Disclosure>
              <Disclosure title={t("inbounds.auto")} sub={t("settings.autoSub")}>
                <AutoSwitch
                  title={t("inbounds.autoPort")}
                  sub={t("inbounds.autoPortSub")}
                  on={autoPort}
                  globalOff={settings.data?.auto_port === false}
                  locked={behindProxy ? t("inbounds.autoPortProxy") : undefined}
                  onChange={set.autoPort}
                />
                {inbound?.dest !== undefined ? (
                  <AutoSwitch
                    title={t("inbounds.autoSni")}
                    sub={t("inbounds.autoSniSub")}
                    on={autoSni}
                    globalOff={settings.data?.auto_sni === false}
                    locked={behindProxy ? t("inbounds.autoSniProxy") : undefined}
                    onChange={set.autoSni}
                  />
                ) : null}
              </Disclosure>
            </>
          ) : tab === "xhttp" ? (
            <XhttpTuning value={xhttp} onChange={(x) => {
              set.xhttp(x);
              clearError("xhttp");
            }} error={errors.xhttp} />
          ) : (
            <Field label={t("inbounds.configLabel")} hint={t("inbounds.configHint")} error={errors.config}>
              <Editor value={config} onChange={editConfig} invalid={!!errors.config} />
              <div className="mt-2 flex flex-wrap items-center gap-3">
                <Button size="sm" loading={validate.isPending} onClick={() => validate.mutate({ config, node_id: inbound?.node_id, inbound_id: inbound?.id, port: port.trim() || undefined })}>
                  {t("inbounds.validate")}
                </Button>
                <Button size="sm" variant="ghost" disabled={!configChanged} onClick={() => inbound && editConfig(inbound.config)}>
                  <RotateCcw size={14} aria-hidden /> {t("inbounds.revert")}
                </Button>
                <ValidateResult v={validate} />
              </div>
            </Field>
          )}
        </div>
      </form>
    </Drawer>
  );
}

/** One switch of the automatic fixes; says so when the global switch is off or why it is
 * locked (shown off). */
function AutoSwitch({
  title,
  sub,
  on,
  globalOff,
  locked,
  onChange,
}: {
  title: string;
  sub: string;
  on: boolean;
  globalOff: boolean;
  locked?: string;
  onChange: (v: boolean) => void;
}) {
  const note = locked ?? (globalOff ? t("inbounds.autoOffGlobal") : sub);
  return <SwitchRow label={title} sub={note} warn={!!locked || globalOff} checked={locked ? false : on} onChange={onChange} disabled={!!locked} />;
}
