import clsx from "clsx";
import { t } from "../../../i18n";
import { FolderMark } from "../user-folders";
import type { Group } from "./model";

/** A small label with the group's name; viaTariff: the client is in it through its tariff. */
export function GroupBadge({ group, viaTariff = false }: { group: Pick<Group, "name" | "color" | "emoji">; viaTariff?: boolean }) {
  const title = viaTariff ? t("groups.badgeTariff", { name: group.name }) : t("groups.badge", { name: group.name });
  return (
    <span className={clsx("fbadge", `fc-${group.color}`)} title={title}>
      <FolderMark folder={group} />
      <span>{group.name}</span>
    </span>
  );
}
