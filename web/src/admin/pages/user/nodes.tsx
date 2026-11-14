import { Server } from "lucide-react";
import type { User } from "../../../api/client";
import { useUserNodes } from "../../../api/hooks";
import { Bar, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { ago, bytes } from "../../../lib/format";
import { nodeLabel } from "../../../lib/node-label";
import { Section } from "./section";

/**
 * The nodes the user went through: the last time on each and the traffic there, the latest
 * first. Counted by user and node from the version that learned to; nothing shows for a user
 * with no traffic counted that way yet.
 */
export function NodesSection({ u }: { u: User }) {
  const nodes = useUserNodes(u.id);
  const list = nodes.data ?? [];
  if (nodes.isSuccess && list.length === 0) return null;
  const top = Math.max(0, ...list.map((n) => n.up + n.down));
  return (
    <Section title={t("userDrawer.nodes.title")} aside={nodes.data ? <span className="num">{list.length}</span> : undefined}>
      {nodes.isPending ? (
        <Skeleton style={{ height: 52, borderRadius: 16 }} />
      ) : nodes.isError ? null : (
        <>
          <ul className="flex flex-col gap-2" aria-label={t("userDrawer.nodes.title")}>
            {list.map((n) => {
              const total = n.up + n.down;
              return (
                <li key={n.node_id} className="flex items-center gap-3">
                  <span className={`grid h-9 w-9 shrink-0 place-items-center rounded-[10px] bg-[var(--hover)] ${n.online ? "text-[var(--leaf-700)]" : "text-[var(--ink-500)]"}`} aria-hidden>
                    <Server size={18} />
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline justify-between gap-2 text-[13px]">
                      <span className="truncate font-medium">{nodeLabel({ name: n.node_name, local: n.local })}</span>
                      <span className="num shrink-0 text-[var(--ink-600)]">{bytes(total)}</span>
                    </div>
                    <Bar pct={top ? (total / top) * 100 : 0} className="mt-1" />
                    <div className="mt-1 text-xs text-[var(--ink-500)]">
                      {n.online ? <span className="text-[var(--leaf-700)]">{t("userDrawer.liveTitle")}</span> : t("userDrawer.nodes.seen", { when: ago(n.last_seen) })}
                      {" · "}
                      <span className="num">
                        ↓ {bytes(n.down)} · ↑ {bytes(n.up)}
                      </span>
                    </div>
                  </div>
                </li>
              );
            })}
          </ul>
          <p className="mt-2 text-xs text-[var(--ink-500)]">{t("userDrawer.nodes.note")}</p>
        </>
      )}
    </Section>
  );
}
