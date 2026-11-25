// The group of a tariff's clients (#290): the buyers and the trial of the tariff are in it
// unless their card gives them one of their own. A group with servers of its own stands in
// for the tariff's.
import { useId } from "react";
import { useGroups } from "../../../api/hooks";
import { Select } from "../../../components/select";
import { Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { emojiName } from "../../../lib/format";

export function ClientGroupField({ value, onChange, error }: { value: number; onChange: (id: number) => void; error?: string }) {
  const uid = useId();
  const groups = useGroups();
  const list = groups.data ?? [];
  // A panel without groups shows nothing of them; a tariff kept in a deleted one shows none.
  if (list.length === 0) return null;
  const chosen = list.find((g) => g.id === value);
  return (
    <Field label={t("tariffs.clientGroup")} htmlFor={uid} hint={chosen && !chosen.servers.as_tariff ? t("tariffs.clientGroupServers", { name: chosen.name }) : t("tariffs.clientGroupHint")} error={error}>
      <Select id={uid} className="input max-w-[320px]" value={String(value)} onChange={(e) => onChange(Number(e.target.value))}>
        <option value="0">{t("tariffs.clientGroupNone")}</option>
        {list.map((g) => (
          <option key={g.id} value={String(g.id)}>
            {emojiName(g)}
          </option>
        ))}
      </Select>
    </Field>
  );
}
