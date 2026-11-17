import { useId } from "react";
import { useSubTemplates } from "../../../../api/hooks";
import { Select } from "../../../../components/select";
import { Field, Skeleton } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { CORES, type Core } from "./model";

/**
 * The group's template of each core: the default of the core for the group's clients. A
 * response rule that names a template still wins; "as for everyone" follows the core's default.
 */
export function TemplatesFields({ value, onChange, errors }: { value: Record<Core, number | null>; onChange: (v: Record<Core, number | null>) => void; errors: Record<string, string> }) {
  const uid = useId();
  const templates = useSubTemplates();
  if (templates.isPending) return <Skeleton style={{ height: 72 }} />;
  const list = templates.data?.templates ?? [];
  return (
    <div className="grid gap-x-3 sm:grid-cols-3">
      {CORES.map((core) => {
        const own = list.filter((x) => x.core === core);
        const def = own.find((x) => x.default);
        const picked = value[core];
        // A template deleted since is the core's default again: the select says so.
        const known = picked == null || own.some((x) => x.id === picked);
        return (
          <Field key={core} label={t(`settings.subTemplates.cores.${core}`)} htmlFor={`${uid}-${core}`} error={errors[`appearance.templates.${core}`]}>
            <Select id={`${uid}-${core}`} className="input" value={known && picked != null ? String(picked) : ""} onChange={(e) => onChange({ ...value, [core]: e.target.value ? Number(e.target.value) : null })}>
              <option value="">{def ? t("groups.appearance.templateDefault", { name: def.name }) : t("groups.appearance.templateAsAll")}</option>
              {own.map((x) => (
                <option key={x.id} value={x.id}>
                  {x.name}
                </option>
              ))}
            </Select>
          </Field>
        );
      })}
    </div>
  );
}
