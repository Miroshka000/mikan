import clsx from "clsx";
import { Pill } from "../../../components/ui";
import { t } from "../../../i18n";
import { nodeLabel } from "../../../lib/node-label";
import type { ServerRow } from "./model";

/** Where a server is: its node, its protocol, its address when it is not the protocol's own, the node it leaves through. */
export function whereOf({ item, inbound, node, exit }: ServerRow): string {
  const parts = [
    node ? nodeLabel(node) : undefined,
    inbound ? (inbound.preset === "custom" ? t("inbounds.customOf", { type: inbound.type }) : inbound.title) : undefined,
    item.default ? undefined : t("serverOrder.address", { address: item.address || t("inbounds.hosts.nodeAddress") }),
    exit && node && exit.id !== node.id ? t("serverOrder.exitVia", { node: nodeLabel(exit) }) : undefined,
  ];
  return parts.filter(Boolean).join(" · ");
}

/** One server of the order: its name in the apps, where it is, and what keeps it from some clients. */
export function ServerRowView({ row, place }: { row: ServerRow; place: number }) {
  const { item, off } = row;
  return (
    <div className={clsx("flex min-w-0 items-center gap-3", off && "opacity-60")}>
      <span className="num w-6 shrink-0 text-center text-xs text-[var(--ink-500)]" aria-hidden>
        {place}
      </span>
      <div className="min-w-0 flex-1">
        <p className="truncate text-[14px] font-medium text-[var(--ink-900)]" title={item.name}>
          {item.name}
        </p>
        <p className="truncate text-xs text-[var(--ink-500)]">{whereOf(row)}</p>
      </div>
      {off || item.cdn || item.for_some ? (
        <div className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
          {off ? <Pill tone="off">{t(`serverOrder.off.${off}`)}</Pill> : null}
          {item.cdn ? <Pill tone="off">{t("serverOrder.cdn")}</Pill> : null}
          {item.for_some ? (
            <span title={t("serverOrder.forSomeHint")}>
              <Pill tone="warn">{t("serverOrder.forSome")}</Pill>
            </span>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
