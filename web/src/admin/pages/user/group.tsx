// The group the client is in (#290): its own, or else its tariff's. The group decides what the
// client sees: its servers stand in for the tariff's, and the addresses for clients are shown
// or kept by group.
import * as Menu from "@radix-ui/react-dropdown-menu";
import { errorText, type User } from "../../../api/client";
import { useGroups, userActions, useTariffs, useUserMutation } from "../../../api/hooks";
import { useToast } from "../../../components/toast";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";
import { GroupBadge } from "../groups/badge";
import { GroupMenuItems } from "../groups/menu-items";
import { Section } from "./section";

export function GroupSection({ u }: { u: User }) {
  const groups = useGroups();
  const tariffs = useTariffs();
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  const list = groups.data ?? [];
  // A panel without groups shows nothing of them: the card stays as it was.
  if (list.length === 0 && u.effective_group_id == null) return null;
  const current = list.find((g) => g.id === u.effective_group_id);
  const tariff = tariffs.data?.find((x) => x.id === u.tariff_id);
  const tariffGroup = list.find((g) => g.id === tariff?.group_id);
  const pick = (id: number | null) =>
    update.mutate(
      // 0 takes the client's own group off: the tariff's applies again.
      { id: u.id, body: { group_id: id ?? 0 } },
      { onSuccess: () => toast.ok(id === null ? t("userDrawer.groupCleared") : t("userDrawer.groupSet")), onError: (e) => toast.error(errorText(e)) },
    );
  const own = u.group_id != null;
  return (
    <Section title={t("userDrawer.group")}>
      <div className="panel-soft flex items-center justify-between gap-3 p-4">
        <div className="min-w-0">
          <div className="text-[13px]">{current ? <GroupBadge group={current} viaTariff={!own} /> : <span className="text-[var(--ink-600)]">{t("userDrawer.noGroup")}</span>}</div>
          <div className="mt-1 text-xs text-[var(--ink-500)]">
            {own ? t("userDrawer.groupOwn") : current ? t("userDrawer.groupViaTariff", { tariff: tariff?.name ?? "" }) : t("userDrawer.groupNoneHint")}
            {current ? ` · ${current.servers.as_tariff ? t("groups.serversTariffShort") : t("groups.serversOwnShort", { n: current.servers.nodes.length + current.servers.inbounds.length })}` : ""}
          </div>
        </div>
        <Menu.Root>
          <Menu.Trigger asChild>
            <Button variant="ghost" size="sm" loading={update.isPending && update.variables?.body.group_id !== undefined}>
              {t("userDrawer.change")}
            </Button>
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content className="menu glass-strong" align="end" sideOffset={6}>
              <GroupMenuItems groups={list} current={own ? u.group_id : null} onPick={pick} />
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      </div>
      {own && tariffGroup && tariffGroup.id !== u.group_id ? <p className="mt-2 text-xs text-[var(--ink-500)]">{t("userDrawer.groupOverTariff", { group: tariffGroup.name })}</p> : null}
    </Section>
  );
}
