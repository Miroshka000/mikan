import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ArrowUpCircle, ArrowUpDown, Check, Pencil, Plus } from "lucide-react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../api/client";
import { qk, useNodes } from "../../api/hooks";
import { CertDrawer } from "../../components/cert-drawer";
import { Confirm } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { useToast } from "../../components/toast";
import { hasTag, TagFilter } from "../../components/tags";
import { Button, EmptyState, PageHeader, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { nodeLabel } from "../../lib/node-label";
import { CascadeDrawer } from "./node-cascade";
import { NodeCheckDrawer, type FixActions } from "./node-check";
import { SpeedDrawer } from "./node-speed";
import { NodeClientsDrawer } from "./node-clients";
import { NodeTrafficDrawer } from "./node-traffic";
import { WarpDrawer } from "./node-warp";
import { BalanceGroups } from "./nodes-balance";
import { NodeCard, updatable, updating } from "./nodes/card";
import { AddNodeDrawer, EditNodeDrawer, KeyDrawer, type Joined } from "./nodes/drawers";

export type Node = Schemas["NodeInfo"];

export function NodesPage() {
  const nodes = useNodes();
  // The palette opens a node's card or the new node's form by the address.
  const search = useSearch({ from: "/_app/nodes" });
  const navigate = useNavigate({ from: "/nodes" });
  const clearSearch = () => void navigate({ search: {}, replace: true });
  const toUpdate = (nodes.data ?? []).filter(updatable);
  const qc = useQueryClient();
  const toast = useToast();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Node | null>(null);
  const [rekeying, setRekeying] = useState<Node | null>(null);
  const [removing, setRemoving] = useState<Node | null>(null);
  const [joined, setJoined] = useState<Joined | null>(null);
  const [warpOf, setWarpOf] = useState<Node | null>(null);
  const [speedOf, setSpeedOf] = useState<Node | null>(null);
  const [trafficOf, setTrafficOf] = useState<Node | null>(null);
  const [clientsOf, setClientsOf] = useState<Node | null>(null);
  const [cascadeOf, setCascadeOf] = useState<Node | null>(null);
  const [certOf, setCertOf] = useState<Node | null>(null);
  const [checkOf, setCheckOf] = useState<Node | null>(null);
  // The arrows that order the nodes show only while ordering: most of the time they are noise.
  const [ordering, setOrdering] = useState(false);
  // One label to show only its nodes. Ordering needs every node, so it shows them all; a
  // label no node has any more filters nothing.
  const [tag, setTag] = useState("");
  const filter = !ordering && tag && nodes.data?.some((n) => hasTag(n.tags, tag)) ? tag : "";
  const many = (nodes.data?.length ?? 0) > 1;
  const [updateOf, setUpdateOf] = useState<Node | "all" | null>(null);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: qk.nodes });
    void qc.invalidateQueries({ queryKey: qk.inbounds });
  };
  const rekey = useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/api/v1/nodes/{id}/key", { params: { path: { id } } })),
    onSuccess: (r) => {
      setRekeying(null);
      setJoined({ id: r.node.id, name: nodeLabel(r.node), key: r.key, command: r.command });
    },
    onSettled: refresh,
    onError: (e) => toast.error(errorText(e)),
  });
  // The node's server does the update (a backup, the signed release, a restart): this only asks.
  const update = useMutation({
    mutationFn: async (id: number | "all") => {
      if (id === "all") await unwrap(api.POST("/api/v1/nodes/update-all"));
      else await unwrap(api.POST("/api/v1/nodes/{id}/update", { params: { path: { id } } }));
    },
    onSuccess: () => {
      toast.ok(t("nodes.updateAsked"));
      setUpdateOf(null);
    },
    onSettled: refresh,
    onError: (e) => {
      toast.error(errorText(e));
      setUpdateOf(null);
    },
  });
  // The order of the servers in the subscriptions. The page shows the new order at once;
  // when the panel refuses, the old one comes back.
  const order = useMutation({
    mutationFn: ({ ids }: { ids: number[]; moved: number }) => unwrap(api.PUT("/api/v1/nodes/order", { body: { ids } })),
    onMutate: async ({ ids }) => {
      await qc.cancelQueries({ queryKey: qk.nodes });
      const prev = qc.getQueryData<Node[]>(qk.nodes);
      if (prev) {
        const byId = new Map(prev.map((n) => [n.id, n]));
        qc.setQueryData<Node[]>(
          qk.nodes,
          ids.flatMap((id) => byId.get(id) ?? []),
        );
      }
      return { prev };
    },
    onError: (e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(qk.nodes, ctx.prev);
      toast.error(errorText(e));
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: qk.nodes }),
  });
  const move = (list: Node[], idx: number, by: -1 | 1) => {
    const ids = list.map((n) => n.id);
    const to = idx + by;
    if (order.isPending || to < 0 || to >= ids.length) return;
    [ids[idx], ids[to]] = [ids[to]!, ids[idx]!];
    order.mutate({ ids, moved: list[idx]!.id });
  };
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/nodes/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      toast.ok(t("nodes.deleted"));
      setRemoving(null);
    },
    onSettled: refresh,
    // A node still in use names what goes through it: all of it, not just the first.
    onError: (e) =>
      toast.error(e instanceof ApiError && e.status === 409 && e.detail === "node_in_use" ? `${t("errors.api.node_in_use")} ${e.messages.join("; ")}` : errorText(e)),
  });

  // The fixes the check and the joining steps offer as buttons.
  const fixesFor = (n: Node | undefined, close: () => void): FixActions =>
    n
      ? {
          rekey: n.local
            ? undefined
            : () => {
                close();
                setRekeying(n);
              },
          update: updatable(n)
            ? () => {
                close();
                setUpdateOf(n);
              }
            : undefined,
          edit: () => {
            close();
            setEditing(n);
          },
        }
      : {};
  const joinedNode = joined ? nodes.data?.find((x) => x.id === joined.id) : undefined;
  // A node named in the address opens once its list is here, as it was then: the card's form
  // is not filled again by every refresh of the list. The address losing it (Back) closes the card.
  const [linked, setLinked] = useState<number | undefined>();
  if (search.node !== linked && (!search.node || nodes.data)) {
    setLinked(search.node);
    const n = search.node ? nodes.data?.find((x) => x.id === search.node) : undefined;
    if (n) setEditing(n);
    else if (!search.node) setEditing(null);
  }
  // A node that is not there (removed, or a hand-made link) leaves the address.
  const missing = !!search.node && !!nodes.data && !nodes.data.some((x) => x.id === search.node);
  useEffect(() => {
    if (missing) void navigate({ search: {}, replace: true });
  }, [missing, navigate]);

  return (
    <>
      <PageHeader
        title={t("nav.nodes")}
        sub={t("nodes.subtitle")}
        actions={
          <>
            {many ? (
              <Button aria-pressed={ordering} onClick={() => setOrdering((o) => !o)} aria-label={ordering ? t("nodes.orderDone") : t("nodes.orderEdit")}>
                {ordering ? <Check size={18} aria-hidden /> : <ArrowUpDown size={18} aria-hidden />}
                <span className="max-[760px]:hidden">{ordering ? t("nodes.orderDone") : t("nodes.orderEdit")}</span>
              </Button>
            ) : null}
            {toUpdate.length > 1 ? (
              <Button loading={update.isPending && update.variables === "all"} disabled={update.isPending || toUpdate.some(updating)} onClick={() => setUpdateOf("all")}>
                <ArrowUpCircle size={18} aria-hidden />
                <span className="max-[760px]:hidden">{t("nodes.updateAll", { n: toUpdate.length })}</span>
              </Button>
            ) : null}
            <Button variant="primary" onClick={() => setAdding(true)}>
              <Plus size={18} aria-hidden />
              <span className="max-[760px]:hidden">{t("nodes.add")}</span>
            </Button>
          </>
        }
      />
      {nodes.data ? <BalanceGroups nodes={nodes.data} /> : null}
      {nodes.data && !ordering ? <TagFilter items={nodes.data} value={filter} onChange={setTag} /> : null}
      <QueryBoundary
        query={nodes}
        pending={
          <div className="grid grid-cols-[minmax(0,1fr)] gap-4 lg:grid-cols-2">
            {[0, 1].map((i) => (
              <Skeleton key={i} style={{ height: 220, borderRadius: 20 }} />
            ))}
          </div>
        }
        wrap={(state) => <section className="card glass">{state}</section>}
      >
        {(list) =>
          list.length === 0 ? (
            <section className="card glass">
              <EmptyState title={t("nodes.emptyTitle")} text={t("nodes.emptyText")} />
            </section>
          ) : (
            <div className="grid grid-cols-[minmax(0,1fr)] gap-4 lg:grid-cols-2">
              {list.length > 1 && list.some((n) => n.local && !n.name) ? (
                <div className="banner warn lg:col-span-2" role="status">
                  <Pencil size={18} className="shrink-0" aria-hidden />
                  <span>{t("nodes.nameLocalHint")}</span>
                </div>
              ) : null}
              {ordering && list.length > 1 ? (
                <p className="banner info lg:col-span-2" role="status">
                  {t("nodes.orderHint")}
                </p>
              ) : null}
              {(filter ? list.filter((n) => hasTag(n.tags, filter)) : list).map((n, idx) => (
                <NodeCard key={n.id} n={n} idx={idx} total={list.length} ordering={ordering} sorting={order.isPending} moving={order.isPending && order.variables.moved === n.id} onMove={(by) => move(list, idx, by)} updating={update.isPending && update.variables === n.id} busy={update.isPending} onUpdate={() => setUpdateOf(n)} onEdit={() => setEditing(n)} onWarp={() => setWarpOf(n)} onSpeed={() => setSpeedOf(n)} onTraffic={() => setTrafficOf(n)} onClients={() => setClientsOf(n)} onCascade={() => setCascadeOf(n)} onCert={() => setCertOf(n)} onCheck={() => setCheckOf(n)} onRekey={() => setRekeying(n)} onRemove={() => setRemoving(n)} />
              ))}
            </div>
          )
        }
      </QueryBoundary>
      <AddNodeDrawer
        open={adding || !!search.add}
        onOpenChange={(v) => {
          setAdding(v);
          if (!v && search.add) clearSearch();
        }}
        onJoined={(j) => {
          setAdding(false);
          if (search.add) clearSearch();
          setJoined(j);
        }}
      />
      <EditNodeDrawer
        node={editing}
        onClose={() => {
          setEditing(null);
          if (search.node) clearSearch();
        }}
      />
      <KeyDrawer joined={joined} onClose={() => setJoined(null)} actions={fixesFor(joinedNode, () => setJoined(null))} />
      <NodeCheckDrawer node={checkOf} onClose={() => setCheckOf(null)} actions={fixesFor(checkOf ?? undefined, () => setCheckOf(null))} />
      <WarpDrawer node={warpOf ? { id: warpOf.id, name: nodeLabel(warpOf) } : null} onClose={() => setWarpOf(null)} />
      <SpeedDrawer node={speedOf ? { id: speedOf.id, name: nodeLabel(speedOf) } : null} onClose={() => setSpeedOf(null)} />
      <NodeTrafficDrawer node={trafficOf ? { id: trafficOf.id, name: nodeLabel(trafficOf) } : null} onClose={() => setTrafficOf(null)} />
      <NodeClientsDrawer node={clientsOf ? { id: clientsOf.id, name: nodeLabel(clientsOf) } : null} onClose={() => setClientsOf(null)} />
      <CascadeDrawer node={cascadeOf ? { id: cascadeOf.id, name: nodeLabel(cascadeOf) } : null} onClose={() => setCascadeOf(null)} />
      <CertDrawer
        open={!!certOf}
        onClose={() => setCertOf(null)}
        title={t("cert.nodeTitle")}
        meta={certOf ? nodeLabel(certOf) : undefined}
        lead={t("cert.nodeLead")}
        current={certOf?.certificate ?? null}
        save={(cert, key) => unwrap(api.PUT("/api/v1/nodes/{id}/certificate", { params: { path: { id: certOf!.id } }, body: { cert, key } })).then(() => qc.invalidateQueries({ queryKey: qk.nodes }))}
        clear={() => unwrap(api.DELETE("/api/v1/nodes/{id}/certificate", { params: { path: { id: certOf!.id } } })).then(() => qc.invalidateQueries({ queryKey: qk.nodes }))}
        clearLabel={t("cert.nodeClear")}
        clearText={t("cert.nodeClearText")}
      />
      <Confirm
        open={!!updateOf && !update.isPending}
        onOpenChange={(v) => !v && setUpdateOf(null)}
        title={updateOf === "all" ? t("nodes.updateAllTitle", { n: toUpdate.length }) : t("nodes.updateTitle", { name: updateOf ? nodeLabel(updateOf) : "" })}
        text={updateOf === "all" ? t("nodes.updateAllText") : t("nodes.updateText")}
        confirm={t("nodes.update")}
        loading={update.isPending}
        onConfirm={() => updateOf && update.mutate(updateOf === "all" ? "all" : updateOf.id)}
      />
      <Confirm
        open={!!rekeying}
        onOpenChange={(v) => !v && setRekeying(null)}
        title={t("nodes.rekeyTitle", { name: rekeying ? nodeLabel(rekeying) : "" })}
        text={t("nodes.rekeyText")}
        confirm={t("nodes.rekeyConfirm")}
        loading={rekey.isPending}
        onConfirm={() => rekeying && rekey.mutate(rekeying.id)}
      />
      <Confirm
        open={!!removing}
        onOpenChange={(v) => !v && setRemoving(null)}
        title={t("nodes.deleteTitle", { name: removing ? nodeLabel(removing) : "" })}
        text={t("nodes.deleteText")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </>
  );
}
