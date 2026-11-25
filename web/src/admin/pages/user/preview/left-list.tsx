import { useNodes } from "../../../../api/hooks";
import { t } from "../../../../i18n";
import { nodeLabel } from "../../../../lib/node-label";
import { leftByReason, type PreviewLeft } from "./model";

/** What of the panel's servers the answer leaves out, grouped by why. */
export function LeftList({ left }: { left: PreviewLeft[] }) {
  const nodes = useNodes();
  if (left.length === 0) return <p className="text-xs text-[var(--ink-500)]">{t("userDrawer.preview.leftNone")}</p>;
  return (
    <div className="flex flex-col gap-3">
      {leftByReason(left).map(([reason, list]) => (
        <section key={reason} aria-label={t(`userDrawer.preview.left.${reason}`)}>
          <h5 className="text-xs font-semibold text-[var(--ink-600)]">{t(`userDrawer.preview.left.${reason}`)}</h5>
          <ul className="mt-1 flex flex-col gap-1">
            {list.map((l, i) => {
              const node = nodes.data?.find((n) => n.id === l.node_id);
              return (
                <li key={`${l.inbound_id}-${l.host_id ?? 0}-${i}`} className="text-[13px]">
                  <span className="font-medium">{l.inbound_name}</span>
                  {l.host_id ? <span className="mono text-[var(--ink-600)]"> · {l.address || t("inbounds.hosts.nodeAddress")}</span> : null}
                  <span className="text-xs text-[var(--ink-500)]">{node ? ` · ${nodeLabel(node)}` : ""}</span>
                  {l.error ? <div className="mono text-xs break-words text-[var(--berry-600)]">{l.error}</div> : null}
                </li>
              );
            })}
          </ul>
        </section>
      ))}
    </div>
  );
}
