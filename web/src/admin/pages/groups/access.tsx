import { errorText } from "../../../api/client";
import { groupActions, useGroupMutation } from "../../../api/hooks";
import { useToast } from "../../../components/toast";
import { t } from "../../../i18n";
import { fieldErrors } from "../../../lib/fields";
import { chipOf } from "../user-folders";
import { GroupForm } from "./form";
import { GroupHosts } from "./hosts";
import { groupServersDraft, type Group } from "./model";

/**
 * What the group sees (#290): its name and look, the servers in place of the tariff's, and
 * the addresses for clients shown by group that name it.
 */
export function GroupAccess({ group }: { group: Group }) {
  const toast = useToast();
  const update = useGroupMutation(groupActions.update);
  const errors = fieldErrors(update.error);
  return (
    <div className="flex flex-col gap-6">
      <GroupForm
        // A saved group's form starts again from what the server keeps.
        key={JSON.stringify([group.name, group.emoji, group.color, group.servers])}
        initial={{ ...chipOf(group), servers: groupServersDraft(group) }}
        busy={update.isPending}
        errors={errors}
        saveLabel={t("common.save")}
        submit={(body) =>
          update.mutate(
            { id: group.id, body },
            {
              onSuccess: () => toast.ok(t("groups.saved")),
              onError: (e) => {
                if (Object.keys(fieldErrors(e)).length === 0) toast.error(errorText(e));
              },
            },
          )
        }
      />
      <section aria-label={t("groups.hostsLabel")}>
        <h4 className="mb-1 text-[13px] font-semibold">{t("groups.hostsTitle")}</h4>
        <p className="mb-2 text-xs text-[var(--ink-500)]">{t("groups.hostsHint")}</p>
        <GroupHosts group={group} />
      </section>
    </div>
  );
}
