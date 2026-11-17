// Who an address for clients is shown to by group (#275): to the clients of some groups alone,
// to every client but theirs, or to everyone. A client's group is its own or its tariff's.
import { useGroups } from "../../../api/hooks";
import { Select } from "../../../components/select";
import { Field, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { FolderMark } from "../user-folders";

/** The modes besides "to everyone" (""), as the API names them. */
export const HOST_GROUP_MODES = ["only", "never"] as const;

export type HostGroups = { groupMode: string; groups: number[] };

/** The groups of a host as the API takes them: none without a mode. */
export function hostGroupsBody(f: HostGroups): { group_mode: string; groups: number[] } {
  return f.groupMode ? { group_mode: f.groupMode, groups: f.groups } : { group_mode: "", groups: [] };
}

export function HostGroupsField({ uid, value, onChange, errors }: { uid: string; value: HostGroups; onChange: (v: HostGroups) => void; errors: Record<string, string> }) {
  const groups = useGroups();
  const list = groups.data ?? [];
  const error = errors.group_mode ?? errors.groups;
  // An address "only" for groups that were all deleted goes to nobody: the admin is told why.
  const orphan = value.groupMode === "only" && list.length > 0 && !value.groups.some((id) => list.some((g) => g.id === id));
  if (list.length === 0 && !value.groupMode) {
    return (
      <Field label={t("inbounds.hosts.groups")} hint={groups.isPending ? undefined : t("inbounds.hosts.groupsNone")}>
        {groups.isPending ? <Skeleton style={{ height: 36, maxWidth: 320 }} /> : null}
      </Field>
    );
  }
  const toggle = (id: number, on: boolean) => onChange({ ...value, groups: on ? [...value.groups, id] : value.groups.filter((x) => x !== id) });
  return (
    <Field label={t("inbounds.hosts.groups")} htmlFor={`${uid}-groups`} hint={orphan ? undefined : t("inbounds.hosts.groupsHint")} error={error ?? (orphan ? t("inbounds.hosts.groupsOrphan") : undefined)}>
      <Select id={`${uid}-groups`} className="input max-w-[320px]" value={value.groupMode} onChange={(e) => onChange({ ...value, groupMode: e.target.value })} aria-invalid={!!error}>
        <option value="">{t("inbounds.hosts.groupModes.all")}</option>
        {HOST_GROUP_MODES.map((m) => (
          <option key={m} value={m}>
            {t(`inbounds.hosts.groupModes.${m}`)}
          </option>
        ))}
      </Select>
      {value.groupMode ? (
        <div className="mt-3 flex flex-wrap gap-x-4 gap-y-2" role="group" aria-label={t("inbounds.hosts.groupsPick")}>
          {list.map((g) => (
            <label key={g.id} className="flex items-center gap-2 text-sm">
              <input type="checkbox" className="check shrink-0" checked={value.groups.includes(g.id)} onChange={(e) => toggle(g.id, e.target.checked)} />
              <FolderMark folder={g} />
              <span>{g.name}</span>
            </label>
          ))}
        </div>
      ) : null}
    </Field>
  );
}
