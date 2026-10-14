// The nodes a user went through, with what they carried over each and when they were last
// seen there: the client's side of the node's client list.
import { errorText, type User } from "../../../api/client";
import { useUserNodes } from "../../../api/hooks";
import { ErrorState, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { ago, bytes } from "../../../lib/format";
import { Section } from "./section";

export function NodesSection({ u }: { u: User }) {
  const nodes = useUserNodes(u.id);
  const items = nodes.data?.items ?? [];
  return (
    <Section title={t("userDrawer.nodes")} aside={items.length > 0 ? t("userDrawer.nodesCount", { n: items.length }) : undefined}>
      {nodes.isPending ? (
        <Skeleton style={{ height: 60, borderRadius: 16 }} />
      ) : nodes.data === undefined ? (
        <ErrorState text={errorText(nodes.error)} onRetry={() => void nodes.refetch()} />
      ) : items.length === 0 ? (
        <p className="text-[13px] text-[var(--ink-500)]">{t("userDrawer.nodesEmpty")}</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {items.map((n) => (
            <li key={n.node_id} className="panel-soft grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 p-3">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="truncate text-[13px] font-medium">{n.public_name || n.name || t("nodes.kindLocal")}</span>
                  {n.online ? <span className="shrink-0 text-[11px] text-[var(--leaf-700)]">{t("users.onlineNow")}</span> : null}
                </div>
                <div className="mt-0.5 text-xs text-[var(--ink-500)]">{n.last_seen ? t("userDrawer.nodeSeen", { ago: ago(n.last_seen) }) : t("userDrawer.nodeNever")}</div>
              </div>
              <div className="text-right">
                <div className="num text-[13px] font-semibold">{bytes(n.bytes)}</div>
                <div className="num text-[11px] text-[var(--ink-400)]">
                  ↓ {bytes(n.down)} · ↑ {bytes(n.up)}
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}
      {items.length > 0 ? <p className="mt-2 text-xs text-[var(--ink-500)]">{t("userDrawer.nodesNote")}</p> : null}
    </Section>
  );
}
