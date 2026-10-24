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
import { ago } from "../../lib/format";
import { nodeLabel } from "../../lib/node-label";
import { Select } from "../../components/select";

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
  // "Check" refetches `c`: the chosen route survives it.
  const {
    draft: { route, exit },
    setDraft,
  } = useDraft<{ route: Route; exit: number }>({ route: (c.relay?.outbound as Route) ?? "direct", exit: c.relay?.exit_node_id ?? 0 });
  const setRoute = (v: Route) => setDraft((d) => ({ ...d, route: v }));
  const setExit = (v: number) => setDraft((d) => ({ ...d, exit: v }));
  const save = useMutation({
    mutationFn: () =>
      unwrap(api.PATCH("/api/v1/nodes/{id}/cascade", { params: { path: { id: nodeId } }, body: { outbound: route, ...(route === "node" ? { exit_node_id: exit } : {}) } })),
    onSuccess: (v) => {
      qc.setQueryData(qk.cascade(nodeId), v);
      toast.ok(t("cascade.saved"));
    },
  });
  const fields = fieldErrors(save.error);
  const changed = route !== ((c.relay?.outbound as Route) ?? "direct") || (route === "node" && exit !== (c.relay?.exit_node_id ?? 0));

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
          <Button variant="primary" loading={save.isPending} disabled={!changed || (route === "node" && !exit)} onClick={() => save.mutate()}>
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
