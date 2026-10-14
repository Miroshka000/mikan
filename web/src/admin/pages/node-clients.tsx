// Who went through one node: each client with what they carried over it and when they were
// last seen there. The panel tells the node's counters apart by user from the version this
// arrived in; what went through before is not there.
import { useNodeClients } from "../../api/hooks";
import { Drawer } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { ago, bytes } from "../../lib/format";

export function NodeClientsDrawer({ node, onClose }: { node: { id: number; name: string } | null; onClose: () => void }) {
  const clients = useNodeClients(node?.id);
  const items = clients.data?.items ?? [];
  const total = clients.data?.total ?? 0;
  return (
    <Drawer open={!!node} onOpenChange={(v) => !v && onClose()} title={t("nodeClients.title")} meta={node?.name}>
      <div className="pt-5">
        <QueryBoundary query={clients} pending={<Skeleton style={{ height: 200, borderRadius: 16 }} />}>
          {() =>
            items.length === 0 ? (
              <p className="text-[13px] text-[var(--ink-500)]">{t("nodeClients.empty")}</p>
            ) : (
              <>
                <ul className="flex flex-col gap-2">
                  {items.map((c) => (
                    <li key={c.user_id} className="panel-soft grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 p-3">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="truncate text-[13px] font-medium">{c.name}</span>
                          {c.online ? <span className="shrink-0 text-[11px] text-[var(--leaf-700)]">{t("nodeClients.online")}</span> : null}
                        </div>
                        <div className="mt-0.5 text-xs text-[var(--ink-500)]">
                          {c.last_seen ? t("nodeClients.lastSeen", { ago: ago(c.last_seen) }) : t("nodeClients.never")}
                        </div>
                      </div>
                      <div className="text-right">
                        <div className="num text-[13px] font-semibold">{bytes(c.bytes)}</div>
                        <div className="num text-[11px] text-[var(--ink-400)]">
                          ↓ {bytes(c.down)} · ↑ {bytes(c.up)}
                        </div>
                      </div>
                    </li>
                  ))}
                </ul>
                {total > items.length ? <p className="mt-3 text-xs text-[var(--ink-500)]">{t("nodeClients.shown", { shown: items.length, total })}</p> : null}
              </>
            )
          }
        </QueryBoundary>
        <p className="mt-6 text-xs text-[var(--ink-500)]">{t("nodeClients.note")}</p>
      </div>
    </Drawer>
  );
}
