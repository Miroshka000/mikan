import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { api, errorText, unwrap, type Inbound, type Schemas } from "../../api/client";
import { qk, useInbounds, useNodes } from "../../api/hooks";
import { Confirm } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { Button, EmptyState, PageHeader, Pill, Segmented, Skeleton } from "../../components/ui";
import { Switch } from "../../components/switch";
import { t, tMaybe } from "../../i18n";
import { ago, maskedAs } from "../../lib/format";
import { nodeLabel } from "../../lib/node-label";
import { hostPort, listenerError } from "./inbound/shared";
import { AddDrawer } from "./inbound/add";
import { EditDrawer } from "./inbound/edit";

export function InboundsPage() {
  const all = useInbounds();
  const nodes = useNodes();
  const qc = useQueryClient();
  const toast = useToast();
  const [picked, setNodeId] = useState<number | null>(null);
  // The panel's own node until the admin picks another one (its id is not a constant).
  const nodeId = picked ?? nodes.data?.find((n) => n.local)?.id ?? nodes.data?.[0]?.id ?? 1;
  const multi = (nodes.data?.length ?? 0) > 1;
  const node = nodes.data?.find((n) => n.id === nodeId);
  // One node: its inbounds are all there is; several: the chosen node's.
  const inbounds = useMemo(() => (all.data ?? []).filter((i) => !multi || i.node_id === nodeId), [all.data, multi, nodeId]);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Inbound | null>(null);
  const [removing, setRemoving] = useState<Inbound | null>(null);
  const patch = useMutation({
    mutationFn: ({ id, body }: { id: number; body: Schemas["PatchInboundInputBody"] }) => unwrap(api.PATCH("/api/v1/inbounds/{id}", { params: { path: { id } }, body })),
    onSettled: () => void qc.invalidateQueries({ queryKey: qk.inbounds }),
    onError: (e) => toast.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/inbounds/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      toast.ok(t("inbounds.deleted"));
      setRemoving(null);
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: qk.inbounds }),
    onError: (e) => toast.error(errorText(e)),
  });

  return (
    <>
      <PageHeader
        title={t("nav.inbounds")}
        sub={t("inbounds.subtitle")}
        actions={
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Plus size={18} aria-hidden />
            <span className="max-[760px]:hidden">{t("common.add")}</span>
          </Button>
        }
      />
      {multi && nodes.data ? (
        nodes.data.length <= 4 ? (
          <div className="max-w-full overflow-x-auto">
            <Segmented value={String(nodeId)} label={t("inbounds.node")} options={nodes.data.map((n) => ({ value: String(n.id), label: nodeLabel(n) }))} onChange={(v) => setNodeId(Number(v))} />
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-3">
            <label htmlFor="inbounds-node" className="text-[13px] font-medium text-[var(--ink-700)]">
              {t("inbounds.node")}
            </label>
            <select id="inbounds-node" className="input max-w-[320px]" value={nodeId} onChange={(e) => setNodeId(Number(e.target.value))}>
              {nodes.data.map((n) => (
                <option key={n.id} value={n.id}>
                  {nodeLabel(n)}
                </option>
              ))}
            </select>
          </div>
        )
      ) : null}
      <QueryBoundary
        query={all}
        pending={
          <div className="grid gap-4 lg:grid-cols-2">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} style={{ height: 150, borderRadius: 20 }} />
            ))}
          </div>
        }
        wrap={(state) => <section className="card glass">{state}</section>}
      >
        {() =>
          inbounds.length === 0 ? (
            <section className="card glass">
              <EmptyState title={multi ? t("inbounds.nodeEmptyTitle") : t("inbounds.emptyTitle")} text={multi ? t("inbounds.nodeEmptyText") : t("inbounds.emptyText")}>
                <Button variant="primary" onClick={() => setAdding(true)}>
                  <Plus size={18} aria-hidden /> {t("inbounds.add")}
                </Button>
              </EmptyState>
            </section>
          ) : (
            <div className="grid gap-4 lg:grid-cols-2">
              {inbounds.map((i, idx) => (
                <section key={i.id} className="card glass reveal flex flex-col" style={{ "--i": idx } as React.CSSProperties}>
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <h2 className="font-display truncate text-lg font-medium tracking-tight">{i.sub_name}</h2>
                      <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--ink-500)]">
                        <span>{i.preset === "custom" ? t("inbounds.customOf", { type: i.type }) : i.title}</span>
                        <span>·</span>
                        <span>{t("inbounds.port", { port: i.listen ? hostPort(i.listen, i.port) : i.port, network: i.network })}</span>
                        <span>·</span>
                        <span className="mono">{i.name}</span>
                      </div>
                    </div>
                    <Switch
                      checked={i.enabled}
                      label={t("inbounds.toggle", { name: i.sub_name })}
                      disabled={patch.isPending}
                      onChange={(v) => patch.mutate({ id: i.id, body: { enabled: v } }, { onSuccess: () => toast.ok(v ? t("inbounds.enabled") : t("inbounds.disabled")) })}
                    />
                  </div>
                  <div className="mt-4 flex flex-wrap items-center gap-2">
                    {!i.enabled ? (
                      <Pill tone="off">{t("inbounds.off")}</Pill>
                    ) : i.status === "ok" ? (
                      <Pill tone="ok">{t("inbounds.working")}</Pill>
                    ) : i.status === "error" ? (
                      <Pill tone="bad">{t("inbounds.error")}</Pill>
                    ) : (
                      <Pill tone="off">{t("inbounds.checking")}</Pill>
                    )}
                    {i.shared ? (
                      <span title={t("inbounds.sharedWarn")}>
                        <Pill tone="warn">{t("inbounds.sharedKey")}</Pill>
                      </span>
                    ) : null}
                    {i.dest ? <span className="text-xs text-[var(--ink-500)]">{t("inbounds.maskedAs", { dest: maskedAs(i) })}</span> : null}
                  </div>
                  {i.apps.length > 0 && i.apps.length < 5 ? (
                    <p className="mt-2 text-xs text-[var(--ink-500)]">{t("inbounds.appsLine", { list: i.apps.map((a) => tMaybe(`inbounds.apps.${a}`) ?? a).join(", ") })}</p>
                  ) : null}
                  {i.enabled && i.node_cert && nodes.data?.find((n) => n.id === i.node_id)?.tls?.pinned ? (
                    <p className="mt-2 text-xs text-[var(--ink-500)]">{t("certs.pinnedNote")}</p>
                  ) : null}
                  {i.status === "error" && i.error ? (
                    <p className="mt-3 text-[13px] text-[var(--berry-600)]" role="alert">
                      {listenerError(i.error)}
                    </p>
                  ) : null}
                  <AutoInfo i={i} />
                  <div className="flex-1" aria-hidden />
                  <div className="mt-4 flex gap-2 border-t border-[var(--hairline)] pt-4">
                    <Button size="sm" onClick={() => setEditing(i)}>
                      <Pencil size={16} aria-hidden /> {t("inbounds.configure")}
                    </Button>
                    <Button size="sm" variant="danger" onClick={() => setRemoving(i)}>
                      <Trash2 size={16} aria-hidden /> {t("common.delete")}
                    </Button>
                  </div>
                </section>
              ))}
            </div>
          )
        }
      </QueryBoundary>
      <AddDrawer open={adding} onOpenChange={setAdding} nodeId={nodeId} nodeName={multi && node ? nodeLabel(node) : undefined} />
      <EditDrawer inbound={editing} onClose={() => setEditing(null)} />
      <Confirm
        open={!!removing}
        onOpenChange={(v) => !v && setRemoving(null)}
        title={t("inbounds.deleteTitle", { name: removing?.sub_name ?? "" })}
        text={t("inbounds.deleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </>
  );
}

/** What the automatic fixes see and last did for a connection. */
function AutoInfo({ i }: { i: Inbound }) {
  const a = i.auto;
  return (
    <>
      {i.enabled && a.cut_off ? (
        <div className="mt-3 flex flex-col items-start gap-1.5" role="status">
          <Pill tone="warn">{a.reached ? t("inbounds.cutOffOf", { n: a.blocked, total: a.blocked + a.reached }) : t("inbounds.cutOff", { n: a.blocked })}</Pill>
          {a.stuck ? <span className="text-xs text-[var(--ink-500)]">{t(`inbounds.stuck.${a.stuck}`)}</span> : null}
        </div>
      ) : null}
      {i.enabled && a.target_ok === false ? (
        <p className="mt-3 text-[13px] text-[var(--berry-600)]" role="alert">
          {t("inbounds.targetDown", { reason: tMaybe(`inbounds.targetErr.${a.target_error ?? ""}`) ?? a.target_error ?? "" })}
        </p>
      ) : null}
      {a.last ? (
        <p className="mt-3 text-xs text-[var(--ink-500)]">
          {t(a.last.kind === "port" ? "inbounds.lastPort" : "inbounds.lastSni", { old: a.last.old, new: a.last.new, ago: ago(a.last.at) })} · {t(`inbounds.reason.${a.last.reason}`)}
        </p>
      ) : null}
    </>
  );
}
