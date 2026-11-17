import * as Menu from "@radix-ui/react-dropdown-menu";
import { Check } from "lucide-react";
import { t } from "../../../i18n";
import { FolderMark } from "../user-folders";
import type { Group } from "./model";

/**
 * The items of a menu that gives a client (or several) a group of its own; `current` is the
 * group of its own now (null: none, the tariff's). "As in the tariff" takes the own group off.
 */
export function GroupMenuItems({ groups, current, onPick }: { groups: Group[]; current?: number | null; onPick: (id: number | null) => void }) {
  return (
    <>
      {groups.map((g) => (
        <Menu.Item key={g.id} className="menu-item" disabled={current === g.id} onSelect={() => onPick(g.id)}>
          <FolderMark folder={g} />
          <span className="min-w-0 flex-1 truncate">{g.name}</span>
          {current === g.id ? <Check size={16} aria-hidden /> : null}
        </Menu.Item>
      ))}
      {groups.length > 0 ? <Menu.Separator className="menu-sep" /> : null}
      <Menu.Item className="menu-item" disabled={current === null} onSelect={() => onPick(null)}>
        <span className="flex-1">{t("groups.asTariff")}</span>
        {current === null ? <Check size={16} aria-hidden /> : null}
      </Menu.Item>
    </>
  );
}
