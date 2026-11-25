// Cascades: client → this node → another node → internet. Inbounds pick their exit in
// their own settings; here a node shows where it sends traffic, who comes through it, and
// where the traffic other nodes relay through it goes next.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, RefreshCw } from "lucide-react";
import { api, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useNodes } from "../../api/hooks";
import { Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Button, Field, Pill, Segmented, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useDraft } from "../../lib/draft";
import { fieldErrors } from "../../lib/fields";
import { ago, destIsIP } from "../../lib/format";
import { nodeLabel } from "../../lib/node-label";
import { Select } from "../../components/select";
import { TargetPicker } from "./inbound/target";

type Cascade = Schemas["CascadeView"];
type Route = "direct" | "warp" | "node";

export function CascadeDrawer({ node, onClose }: { node: { id: number; name: string } | null; onClose: () => void }) {
  const cascade = useQuery({
    queryKey: qk.cascade(node?.id ?? 0),
    queryFn: ({ signal }) => unwrap(api.GET("/api/v1/nodes/{id}/cascade", { params: { path: { id: node!.id } }, signal })),
    enabled: !!node,
  });
  return (
    <Drawer open={!!node} onOpenChange={(v) => !v && onClose()} title={t("cascade.title")} meta={node?.name}>
      <div className="pt-5">
        <p className="mb-4 text-[13px] text-[var(--ink-600)]">{t("cascade.intro")}</p>
        <QueryBoundary query={cascade} pending={<Skeleton style={{ height: 240, borderRadius: 16 }} />}>
          {(c) => <Body key={node!.id} nodeId={node!.id} c={c} refetch={() => void cascade.refetch()} checking={cascade.isFetching} />}
        </QueryBoundary>
      </div>
    </Drawer>
  );
}

function Body({ nodeId, c, refetch, checking }: { nodeId: number; c: Cascade; refetch: () => void; checking: boolean }) {
  const qc = useQueryClient();
  const toast = useToast();
  const nodes = useNodes();
  const name = (id: number) => {
    const n = nodes.data?.find((x) => x.id === id);
    return n ? nodeLabel(n) : `#${id}`;
  };
  // "Check" refetches `c`: the chosen route and site survive it.
  const relay = c.relay;
  const {
    draft: { route, exit, auto, dest, sni, autoPort, port },
    setDraft,
  } = useDraft<{ route: Route; exit: number; auto: boolean; dest: string; sni: string; autoPort: boolean; port: string }>({
    route: (relay?.outbound as Route) ?? "direct",
    exit: relay?.exit_node_id ?? 0,
    auto: relay?.auto_sni ?? true,
    dest: relay?.dest ?? "",
    sni: relay?.server_name ?? "",
    autoPort: relay?.auto_port ?? true,
    port: relay?.port ?? "",
  });
  const setRoute = (v: Route) => setDraft((d) => ({ ...d, route: v }));
  const setExit = (v: number) => setDraft((d) => ({ ...d, exit: v }));
  // The relay's site goes with the route only when it changed: auto back, or the admin's own.
  const targetChanged =
    !!relay && (auto !== relay.auto_sni || (!auto && (dest.trim() !== relay.dest || (destIsIP(dest) && sni.trim() !== relay.server_name))));
  const portChanged = !!relay && (autoPort !== relay.auto_port || (!autoPort && port.trim() !== relay.port));
  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PATCH("/api/v1/nodes/{id}/cascade", {
          params: { path: { id: nodeId } },
          body: {
            outbound: route,
            ...(route === "node" ? { exit_node_id: exit } : {}),
            ...(targetChanged ? (auto ? { auto_sni: true } : { auto_sni: false, dest: dest.trim(), ...(destIsIP(dest) ? { server_name: sni.trim() } : {}) }) : {}),
            ...(portChanged ? (autoPort ? { auto_port: true } : { auto_port: false, port: port.trim() }) : {}),
          },
        }),
      ),
    onSuccess: (v) => {
      qc.setQueryData(qk.cascade(nodeId), v);
      setDraft((d) => ({
        ...d,
        auto: v.relay?.auto_sni ?? true,
        dest: v.relay?.dest ?? "",
        sni: v.relay?.server_name ?? "",
        autoPort: v.relay?.auto_port ?? true,
        port: v.relay?.port ?? "",
      }));
      toast.ok(t("cascade.saved"));
    },
  });
  const fields = fieldErrors(save.error);
  const changed =
    route !== ((relay?.outbound as Route) ?? "direct") || (route === "node" && exit !== (relay?.exit_node_id ?? 0)) || targetChanged || portChanged;
  const siteMissing = !!relay && !auto && (!dest.trim() || (destIsIP(dest) && !sni.trim()));
  const portMissing = !!relay && !autoPort && !port.trim();

  return (
    <>
      <section className="mb-5">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-[13px] font-semibold">{t("cascade.exits")}</h3>
          {c.exits.length ? (
            <Button size="sm" variant="ghost" loading={checking} onClick={refetch}>
              <RefreshCw size={14} aria-hidden /> {t("cascade.check")}
            </Button>
          ) : null}
        </div>
        {c.exits.length === 0 ? (
          <p className="text-xs text-[var(--ink-500)]">{t("cascade.exitsNone")}</p>
        ) : (
          <ul className="row-list">
            {c.exits.map((e) => (
              <li key={e.node_id} className="py-3">
                <div className="flex flex-wrap items-center gap-2 text-[13px]">
                  <ArrowRight size={14} aria-hidden className="text-[var(--ink-400)]" />
                  <b>{e.name || name(e.node_id)}</b>
                  {e.probe ? (
                    e.probe.ok ? (
                      <Pill tone="ok">{t("cascade.exitIP", { ip: e.probe.ip ?? "" })}</Pill>
                    ) : (
                      <Pill tone="bad">{t("cascade.down")}</Pill>
                    )
                  ) : (
                    <Pill tone="off">{t("cascade.unknown")}</Pill>
                  )}
                </div>
                <div className="mt-1 text-xs text-[var(--ink-500)]">
                  {[...e.inbounds, ...(e.relay ? [t("cascade.relayed")] : [])].join(", ")}
                  {e.probe ? ` · ${ago(e.probe.checked_at)}` : ""}
                </div>
              </li>
            ))}
          </ul>
        )}
        <p className="mt-2 text-xs text-[var(--ink-500)]">{t("cascade.exitsHint")}</p>
      </section>

      <section className="border-t border-[var(--hairline)] pt-4">
        <h3 className="mb-1 text-[13px] font-semibold">{t("cascade.relay")}</h3>
        <p className="mb-3 text-xs text-[var(--ink-500)]">
          {c.relay && c.relay.sources.length
            ? t("cascade.sources", { list: c.relay.sources.map((s) => s.name || name(s.node_id)).join(", "), port: c.relay.port })
            : t("cascade.sourcesNone")}
        </p>
        {c.relay ? <RelayListener relay={c.relay} /> : null}
        {relay ? (
          <>
            <Field label={t("cascade.target.label")} hint={t("cascade.target.hint")}>
              <Segmented
                label={t("cascade.target.label")}
                value={auto ? "auto" : "manual"}
                onChange={(v) => setDraft((d) => ({ ...d, auto: v === "auto" }))}
                options={[
                  { value: "auto", label: t("cascade.target.auto") },
                  { value: "manual", label: t("cascade.target.manual") },
                ]}
              />
            </Field>
            {auto ? (
              <p className="-mt-2 mb-4 text-xs text-[var(--ink-500)]">
                {relay.auto_sni ? t(`cascade.target.source.${relay.target}`, { site: relay.server_name }) : t("cascade.target.autoNext")}
              </p>
            ) : (
              <>
                <Field label={t("inbounds.dest")} htmlFor="cascade-dest" hint={destIsIP(dest) ? t("inbounds.destHintIP") : t("inbounds.destHint")} error={fields.dest}>
                  <input
                    id="cascade-dest"
                    className="input mono"
                    value={dest}
                    onChange={(e) => setDraft((d) => ({ ...d, dest: e.target.value }))}
                    placeholder="www.example.com:443"
                    aria-invalid={!!fields.dest}
                    spellCheck={false}
                    autoComplete="off"
                  />
                </Field>
                {destIsIP(dest) ? (
                  <Field label={t("inbounds.sniLabel")} htmlFor="cascade-sni" hint={t("inbounds.sniHint")} error={fields.server_name}>
                    <input
                      id="cascade-sni"
                      className="input mono"
                      value={sni}
                      onChange={(e) => setDraft((d) => ({ ...d, sni: e.target.value }))}
                      placeholder="example.com"
                      aria-invalid={!!fields.server_name}
                      spellCheck={false}
                      autoComplete="off"
                    />
                  </Field>
                ) : null}
                <TargetPicker dest={dest} sni={sni} nodeId={nodeId} onPick={(d, s) => setDraft((x) => ({ ...x, dest: d, sni: s }))} />
              </>
            )}
            <Field label={t("cascade.port.label")} hint={t("cascade.port.hint")}>
              <Segmented
                label={t("cascade.port.label")}
                value={autoPort ? "auto" : "manual"}
                onChange={(v) => setDraft((d) => ({ ...d, autoPort: v === "auto" }))}
                options={[
                  { value: "auto", label: t("cascade.port.auto") },
                  { value: "manual", label: t("cascade.port.manual") },
                ]}
              />
            </Field>
            {autoPort ? (
              <p className="-mt-2 mb-4 text-xs text-[var(--ink-500)]">{t(relay.auto_port ? "cascade.port.autoNow" : "cascade.port.autoNext", { port: relay.port })}</p>
            ) : (
              <Field label={t("cascade.port.manualLabel")} htmlFor="cascade-port" hint={t("cascade.port.manualHint")} error={fields.port}>
                <input
                  id="cascade-port"
                  className="input mono max-w-[160px]"
                  inputMode="numeric"
                  value={port}
                  onChange={(e) => setDraft((d) => ({ ...d, port: e.target.value }))}
                  placeholder="443"
                  aria-invalid={!!fields.port}
                  autoComplete="off"
                />
              </Field>
            )}
          </>
        ) : null}
        {save.error && !Object.keys(fields).length ? <div className="banner err mb-4" role="alert">{errorText(save.error)}</div> : null}
        <Field label={t("cascade.route")} hint={t(`cascade.routeHint.${route}`)} error={fields.exit_node_id}>
          <Segmented
            label={t("cascade.route")}
            value={route}
            onChange={setRoute}
            options={[
              { value: "direct", label: t("inbounds.outboundDirect") },
              { value: "warp", label: "WARP" },
              { value: "node", label: t("inbounds.outboundNode") },
            ]}
          />
          {route === "node" ? (
            <Select className="input mt-2 max-w-[320px]" value={exit} onChange={(e) => setExit(Number(e.target.value))} aria-label={t("inbounds.exitNode")}>
              <option value={0} disabled>
                {t("inbounds.exitPick")}
              </option>
              {(nodes.data ?? [])
                .filter((n) => n.id !== nodeId)
                .map((n) => (
                  <option key={n.id} value={n.id} disabled={!n.enabled}>
                    {nodeLabel(n)}
                  </option>
                ))}
            </Select>
          ) : null}
        </Field>
        <div className="form-actions">
          <Button variant="primary" loading={save.isPending} disabled={!changed || (route === "node" && !exit) || siteMissing || portMissing} onClick={() => save.mutate()}>
            {t("common.save")}
          </Button>
        </div>
      </section>
    </>
  );
}

// The relay's listener on the node. A port another program holds keeps it from starting,
// and the nodes that leave through it then reach that program instead.
function RelayListener({ relay }: { relay: NonNullable<Cascade["relay"]> }) {
  const state = relay.listener;
  if (state === "unknown") return null;
  return (
    <div className="mb-3">
      <Pill tone={state === "ok" ? "ok" : "bad"}>{t(`cascade.listener.${state}`, { port: relay.port })}</Pill>
      {state === "ok" ? null : (
        <div className="banner err mt-2" role="alert">
          <div>
            {t(state === "busy" ? "cascade.listener.busyText" : "cascade.listener.failedText", { port: relay.port })}
            {relay.error ? <div className="mt-1 break-all font-mono text-xs opacity-80">{relay.error}</div> : null}
          </div>
        </div>
      )}
    </div>
  );
}
