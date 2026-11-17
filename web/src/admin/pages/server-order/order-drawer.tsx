import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Layers, Route } from "lucide-react";
import { useMemo, useState } from "react";
import { api, ApiError, errorText, unwrap } from "../../../api/client";
import { qk, useInbounds, useNodes, useServerOrder } from "../../../api/hooks";
import { Confirm, Drawer } from "../../../components/overlay";
import { SortableList } from "../../../components/sortable";
import { useToast } from "../../../components/toast";
import { Button, EmptyState, ErrorState, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { byExit, byNodes, rowsOf, sameOrder } from "./model";
import { ServerRowView, whereOf } from "./server-row";

/**
 * «Порядок в подписке»: every server the subscriptions give, each address of each protocol
 * across the nodes, in one list the admin drags into order. The order is a draft until it is
 * saved; a node then goes where its first server does.
 */
export function ServerOrderDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const order = useServerOrder(open);
  const inbounds = useInbounds();
  const nodes = useNodes();
  const qc = useQueryClient();
  const toast = useToast();
  // null: the order as the panel has it.
  const [draft, setDraft] = useState<number[] | null>(null);
  const [leaving, setLeaving] = useState(false);
  const rows = useMemo(() => rowsOf(order.data ?? [], inbounds.data ?? [], nodes.data ?? []), [order.data, inbounds.data, nodes.data]);
  const saved = useMemo(() => (order.data ?? []).map((i) => i.host_id), [order.data]);
  // A server gone meanwhile leaves the draft; one added shows once the draft is saved or dropped.
  const ids = (draft ?? saved).filter((id) => rows.has(id));
  const dirty = draft !== null && !sameOrder(ids, saved);
  const nodeOrder = (nodes.data ?? []).map((n) => n.id);
  const cascades = (inbounds.data ?? []).some((i) => i.exit_node_id);
  const close = () => {
    setDraft(null);
    setLeaving(false);
    onClose();
  };
  const save = useMutation({
    mutationFn: (host_ids: number[]) => unwrap(api.PUT("/api/v1/inbounds/order", { body: { host_ids } })),
    onSuccess: () => {
      toast.ok(t("serverOrder.saved"));
      close();
    },
    onError: (e) => {
      // Servers came or went while the admin was ordering: the list is read again.
      if (e instanceof ApiError && e.codes["host_ids"] === "host_order_mismatch") {
        setDraft(null);
        toast.error(t("serverOrder.stale"));
        return;
      }
      toast.error(errorText(e));
    },
    // The nodes follow their first servers: their list moves too.
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.serverOrder });
      void qc.invalidateQueries({ queryKey: qk.nodes });
    },
  });
  const nodeCount = new Set((order.data ?? []).map((i) => i.node_id)).size;

  return (
    <>
      <Drawer
        open={open}
        onOpenChange={(v) => {
          if (v) return;
          if (dirty && !save.isPending) setLeaving(true);
          else if (!save.isPending) close();
        }}
        title={t("serverOrder.title")}
        meta={order.data && order.data.length > 0 ? t("serverOrder.meta", { n: order.data.length, nodes: nodeCount }) : undefined}
        wide
        layoutScroll
        footer={
          order.data && order.data.length > 1 ? (
            <>
              {dirty ? (
                <span className="mr-auto self-center text-xs text-[var(--ink-500)]" role="status">
                  {t("reorder.unsaved")}
                </span>
              ) : null}
              <Button variant="ghost" disabled={save.isPending} onClick={() => (dirty ? setLeaving(true) : close())}>
                {t("common.cancel")}
              </Button>
              <Button variant="primary" loading={save.isPending} disabled={!dirty} onClick={() => save.mutate(ids)}>
                {t("reorder.save")}
              </Button>
            </>
          ) : undefined
        }
      >
        <p className="muted mt-2 text-[13px]">{t("serverOrder.lead")}</p>
        {order.data === undefined ? (
          order.isError ? (
            <ErrorState text={errorText(order.error)} onRetry={() => void order.refetch()} />
          ) : (
            <div className="mt-4 flex flex-col gap-2" aria-busy>
              {[0, 1, 2, 3, 4].map((i) => (
                <Skeleton key={i} style={{ height: 56, borderRadius: 16 }} />
              ))}
            </div>
          )
        ) : order.data.length === 0 ? (
          <EmptyState title={t("serverOrder.emptyTitle")} text={t("serverOrder.emptyText")} icon={Layers} />
        ) : (
          <>
            {order.data.length > 1 ? (
              <div className="mt-4 mb-3 flex flex-wrap items-center gap-2" role="group" aria-label={t("serverOrder.arrange")}>
                <span className="text-xs font-medium text-[var(--ink-600)]">{t("serverOrder.arrange")}</span>
                <Button size="sm" disabled={save.isPending} title={t("serverOrder.byNodesHint")} onClick={() => setDraft(byNodes(ids, rows, nodeOrder))}>
                  <Layers size={16} aria-hidden /> {t("serverOrder.byNodes")}
                </Button>
                {cascades ? (
                  <Button size="sm" disabled={save.isPending} title={t("serverOrder.byExitHint")} onClick={() => setDraft(byExit(ids, rows, nodeOrder))}>
                    <Route size={16} aria-hidden /> {t("serverOrder.byExit")}
                  </Button>
                ) : null}
                <span className="basis-full text-xs text-[var(--ink-500)]">{t("serverOrder.howTo")}</span>
              </div>
            ) : null}
            <SortableList
              values={ids}
              onReorder={setDraft}
              label={t("serverOrder.title")}
              disabled={save.isPending}
              nameOf={(id) => {
                const row = rows.get(id);
                return row ? `${row.item.name}, ${whereOf(row)}` : String(id);
              }}
            >
              {(id, i) => {
                const row = rows.get(id);
                return row ? <ServerRowView row={row} place={i + 1} /> : null;
              }}
            </SortableList>
          </>
        )}
      </Drawer>
      <Confirm
        open={leaving}
        onOpenChange={setLeaving}
        title={t("reorder.discardTitle")}
        text={t("reorder.discardText")}
        confirm={t("reorder.discard")}
        danger
        onConfirm={close}
      />
    </>
  );
}
