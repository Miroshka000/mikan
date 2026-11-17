import { ArrowDown, ArrowUp, Pencil, Trash2 } from "lucide-react";
import { t } from "../../../i18n";
import { FolderMark } from "../user-folders";
import { ownParts } from "./appearance/model";
import type { Group } from "./model";

/** What a group gives in one line: its clients, its tariffs, its servers and how much of its look is its own. */
function summary(g: Group): string {
  const parts = [t("groups.users", { n: g.users })];
  if (g.tariffs > 0) parts.push(t("groups.tariffs", { n: g.tariffs }));
  parts.push(g.servers.as_tariff ? t("groups.serversTariffShort") : t("groups.serversOwnShort", { n: g.servers.nodes.length + g.servers.inbounds.length }));
  if (g.hosts.length > 0) parts.push(t("groups.hostsShort", { n: g.hosts.length }));
  const own = ownParts(g.appearance);
  if (own > 0) parts.push(t("groups.appearanceShort", { n: own }));
  return parts.join(" · ");
}

/** A group in the list: its mark, name and summary, with its order, edit and delete buttons. */
export function GroupRow({
  group,
  first,
  last,
  ordering,
  onMove,
  onEdit,
  onDelete,
}: {
  group: Group;
  first: boolean;
  last: boolean;
  ordering: boolean;
  onMove?: (by: -1 | 1) => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const g = group;
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
      <div className="flex min-w-0 items-center gap-3">
        <FolderMark folder={g} />
        <div className="min-w-0">
          <div className="truncate text-[13px] font-semibold">{g.name}</div>
          <div className="text-xs text-[var(--ink-500)]">{summary(g)}</div>
        </div>
      </div>
      <div className="flex items-center gap-1">
        {onMove ? (
          <>
            <button type="button" className="icon-btn" disabled={ordering || first} aria-label={t("folders.moveUp", { name: g.name })} title={t("folders.moveUp", { name: g.name })} onClick={() => onMove(-1)}>
              <ArrowUp size={16} aria-hidden />
            </button>
            <button type="button" className="icon-btn" disabled={ordering || last} aria-label={t("folders.moveDown", { name: g.name })} title={t("folders.moveDown", { name: g.name })} onClick={() => onMove(1)}>
              <ArrowDown size={16} aria-hidden />
            </button>
          </>
        ) : null}
        <button type="button" className="icon-btn" aria-label={t("groups.edit", { name: g.name })} title={t("groups.edit", { name: g.name })} onClick={onEdit}>
          <Pencil size={16} aria-hidden />
        </button>
        <button type="button" className="icon-btn" aria-label={t("groups.delete", { name: g.name })} title={t("groups.delete", { name: g.name })} onClick={onDelete}>
          <Trash2 size={16} aria-hidden />
        </button>
      </div>
    </div>
  );
}
