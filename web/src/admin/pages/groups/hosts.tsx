import { Link } from "@tanstack/react-router";
import { useInbounds, useNodes } from "../../../api/hooks";
import { t } from "../../../i18n";
import { nodeLabel } from "../../../lib/node-label";
import type { Group } from "./model";

/**
 * The addresses for clients shown by group that name this one: only to it (and the others
 * named), or to everyone but it. They are changed at the address, beside its states and trial.
 */
export function GroupHosts({ group }: { group: Group }) {
  const nodes = useNodes();
  // The name the subscriptions show; the panel's own one while the list loads.
  const inbounds = useInbounds();
  if (group.hosts.length === 0) {
    return <p className="text-xs text-[var(--ink-500)]">{t("groups.hostsNone")}</p>;
  }
  return (
    <ul className="row-list" aria-label={t("groups.hostsLabel")}>
      {group.hosts.map((h) => {
        const node = nodes.data?.find((n) => n.id === h.node_id);
        const inbound = inbounds.data?.find((i) => i.id === h.inbound_id);
        return (
          <li key={h.id} className="flex items-center justify-between gap-3 py-2">
            <div className="min-w-0">
              <div className="truncate text-[13px] font-medium">
                <span className="mono">{h.address || t("inbounds.hosts.nodeAddress")}</span>
                {h.name ? <span className="text-[var(--ink-500)]"> · {h.name}</span> : null}
              </div>
              <div className="truncate text-xs text-[var(--ink-500)]">
                {inbound?.sub_name || h.inbound_name}
                {node ? ` · ${nodeLabel(node)}` : ""}
                {h.default ? ` · ${t("inbounds.hosts.default")}` : ""}
              </div>
            </div>
            <span className="tag shrink-0">{t(`groups.hostModes.${h.mode}`)}</span>
          </li>
        );
      })}
      <li className="pt-2">
        <Link to="/inbounds" className="link-btn text-xs">
          {t("groups.hostsEdit")}
        </Link>
      </li>
    </ul>
  );
}
