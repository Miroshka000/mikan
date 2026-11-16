import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ListOrdered, TriangleAlert } from "lucide-react";
import { useMemo, useState } from "react";
import { api, ApiError, errorText, unwrap, type Schemas } from "../../../api/client";
import { qk, useServerOrder } from "../../../api/hooks";
import { Confirm } from "../../../components/overlay";
import { SortableList } from "../../../components/sortable";
import { useToast } from "../../../components/toast";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";
import { nodeLabel } from "../../../lib/node-label";
import { inBlocks, sameOrder } from "../server-order/model";
import { NodeStatus } from "./card";

type Node = Schemas["NodeInfo"];

/**
 * The order of the nodes, dragged into place: each node's servers go in the subscriptions as
 * one block, the nodes in this order. Servers mixed across the nodes («Порядок в подписке»)
 * are put together by node, after a question. The order is a draft until it is saved.
 */
export function NodeOrderPanel({ nodes, onDone, onServers }: { nodes: Node[]; onDone: () => void; onServers: () => void }) {
  const servers = useServerOrder();
  const qc = useQueryClient();
  const toast = useToast();
  // null: the order as the panel has it.
  const [draft, setDraft] = useState<number[] | null>(null);
  const [asking, setAsking] = useState(false);
  const byId = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  const saved = useMemo(() => nodes.map((n) => n.id), [nodes]);
  const ids = (draft ?? saved).filter((id) => byId.has(id));
  const dirty = draft !== null && !sameOrder(ids, saved);
  // The servers' order is mixed across the nodes: saving puts each node's together.
  const mixed = servers.data ? !inBlocks(servers.data.map((s) => s.node_id)) : false;
  const counts = useMemo(() => {
    const out = new Map<number, number>();
    for (const s of servers.data ?? []) out.set(s.node_id, (out.get(s.node_id) ?? 0) + 1);
    return out;
  }, [servers.data]);
  const save = useMutation({
    mutationFn: (list: number[]) => unwrap(api.PUT("/api/v1/nodes/order", { body: { ids: list } })),
    onSuccess: () => {
      toast.ok(t("reorder.saved"));
      setAsking(false);
      setDraft(null);
      onDone();
    },
    onError: (e) => {
      setAsking(false);
      // A node came or went meanwhile: the draft gives way to the list as it is now.
      if (e instanceof ApiError && e.codes["ids"] === "node_order_mismatch") setDraft(null);
      toast.error(errorText(e));
    },
    // The servers move with their nodes.
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.nodes });
      void qc.invalidateQueries({ queryKey: qk.serverOrder });
    },
  });

  return (
    <section className="card glass">
      <p className="text-[13px] text-[var(--ink-600)]">{t("nodes.orderHint")}</p>
      {mixed ? (
        <div className="banner warn mt-3" role="status">
          <TriangleAlert size={16} className="shrink-0" aria-hidden />
          <span>{t("nodes.orderMixed")}</span>
        </div>
      ) : null}
      <SortableList
        values={ids}
        onReorder={setDraft}
        label={t("nodes.orderList")}
        disabled={save.isPending}
        className="mt-4"
        nameOf={(id) => {
          const n = byId.get(id);
          return n ? nodeLabel(n) : String(id);
        }}
      >
        {(id, i) => {
          const n = byId.get(id);
          return n ? <NodeOrderRow n={n} place={i + 1} servers={servers.data ? (counts.get(id) ?? 0) : undefined} /> : null;
        }}
      </SortableList>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Button onClick={onServers}>
          <ListOrdered size={18} aria-hidden /> {t("nodes.orderServers")}
        </Button>
        <span className="flex-1" />
        {dirty ? (
          <span className="text-xs text-[var(--ink-500)]" role="status">
            {t("reorder.unsaved")}
          </span>
        ) : null}
        <Button
          variant="ghost"
          disabled={save.isPending}
          onClick={() => {
            setDraft(null);
            onDone();
          }}
        >
          {t("common.cancel")}
        </Button>
        <Button variant="primary" loading={save.isPending && !asking} disabled={!dirty} onClick={() => (mixed ? setAsking(true) : save.mutate(ids))}>
          {t("reorder.save")}
        </Button>
      </div>
      <Confirm
        open={asking}
        onOpenChange={(v) => !v && !save.isPending && setAsking(false)}
        title={t("nodes.orderMixedTitle")}
        text={t("nodes.orderMixedText")}
        confirm={t("nodes.orderMixedConfirm")}
        loading={save.isPending}
        onConfirm={() => save.mutate(ids)}
      />
    </section>
  );
}

/** One node of the order: its name, where it is, how many servers it gives. */
function NodeOrderRow({ n, place, servers }: { n: Node; place: number; servers?: number }) {
  return (
    <div className="flex min-w-0 items-center gap-3">
      <span className="num w-6 shrink-0 text-center text-xs text-[var(--ink-500)]" aria-hidden>
        {place}
      </span>
      <div className="min-w-0 flex-1">
        <p className="truncate text-[14px] font-medium text-[var(--ink-900)]">{nodeLabel(n)}</p>
        <p className="truncate text-xs text-[var(--ink-500)]">
          <span className="mono">{n.host || "—"}</span>
          {servers !== undefined ? ` · ${t("nodes.orderServersCount", { n: servers })}` : null}
        </p>
      </div>
      <div className="shrink-0">
        <NodeStatus n={n} />
      </div>
    </div>
  );
}
