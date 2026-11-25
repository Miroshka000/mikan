import { useId } from "react";
import { useSettings } from "../../../../api/hooks";
import { Select } from "../../../../components/select";
import { SwitchRow } from "../../../../components/switch";
import { Field } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { UnbindRulesEditor } from "../../settings/unbind-rules";
import type { AppearanceDraft, Tristate } from "./model";

type Set = (patch: Partial<AppearanceDraft>) => void;

/**
 * The group's rules of devices: whether an app without a device id gets the servers, and how
 * its subscribers unbind devices themselves. Binding devices stays the panel's: with it off,
 * these do nothing, and the section says so.
 */
export function DevicesFields({ draft, set, errors }: { draft: AppearanceDraft; set: Set; errors: Record<string, string> }) {
  const uid = useId();
  const panel = useSettings().data;
  const binding = panel?.device_binding ?? true;
  const asAll = panel ? (panel.device_require_hwid ? t("groups.appearance.hwidAsAllOn") : t("groups.appearance.hwidAsAllOff")) : t("groups.appearance.asAll");
  return (
    <>
      {binding ? null : <p className="mb-3 text-xs text-[var(--honey-600)]">{t("groups.appearance.bindingOff")}</p>}
      <Field label={t("settings.requireHwid")} htmlFor={`${uid}-hwid`} hint={t("settings.requireHwidSub")} error={errors["appearance.require_hwid"]}>
        <Select id={`${uid}-hwid`} className="input max-w-[320px]" value={draft.requireHwid} onChange={(e) => set({ requireHwid: e.target.value as Tristate })}>
          <option value="">{asAll}</option>
          <option value="on">{t("groups.appearance.hwidOn")}</option>
          <option value="off">{t("groups.appearance.hwidOff")}</option>
        </Select>
      </Field>
      <SwitchRow label={t("groups.appearance.unbindOwn")} sub={draft.unbindOwn ? t("settings.unbindSub") : t("groups.appearance.unbindAsAll")} checked={draft.unbindOwn} onChange={(unbindOwn) => set({ unbindOwn })} />
      {draft.unbindOwn ? (
        <div className="mt-2">
          <UnbindRulesEditor
            value={draft.unbind}
            busy={false}
            onChange={(patch, done) => {
              set({ unbind: { ...draft.unbind, ...patch } });
              done();
            }}
          />
          {Object.keys(errors).some((k) => k.startsWith("appearance.unbind")) ? (
            <p className="mt-2 text-xs text-[var(--berry-600)]" role="alert">
              {Object.entries(errors).find(([k]) => k.startsWith("appearance.unbind"))?.[1]}
            </p>
          ) : null}
        </div>
      ) : null}
    </>
  );
}
