import { useId, useState } from "react";
import { useSettings } from "../../../../api/hooks";
import { SwitchRow } from "../../../../components/switch";
import { Field } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { TITLE_VARS, unknownVars } from "../../../../lib/title-vars";
import { updateHours, type AppearanceDraft } from "./model";

type Set = (patch: Partial<AppearanceDraft>) => void;

/**
 * What the apps show of the group's subscription: its title, the announcement and its link,
 * the support link and how often the apps fetch it again. An empty field is the panel's
 * setting, which the field shows greyed; the announcement may be turned off for the group.
 */
export function TextsFields({ draft, set, errors }: { draft: AppearanceDraft; set: Set; errors: Record<string, string> }) {
  const uid = useId();
  const panel = useSettings().data;
  // A variable chip goes into the field last typed in: the title until the announcement is.
  const [target, setTarget] = useState<"title" | "announce">("title");
  const insert = (v: string) => {
    const next = (draft[target] ? draft[target].replace(/\s*$/, " ") : "") + `{${v}}`;
    set(target === "title" ? { title: next } : { announce: next });
  };
  const unknown = [...unknownVars(draft.title), ...unknownVars(draft.noAnnounce ? "" : draft.announce)];
  const hours = draft.updateHours.trim();
  return (
    <>
      <Field label={t("settings.subTitle")} htmlFor={`${uid}-title`} hint={t("groups.appearance.panelHint")} error={errors["appearance.title"]}>
        <input
          id={`${uid}-title`}
          className="input"
          value={draft.title}
          maxLength={200}
          placeholder={panel?.sub_title || panel?.brand || "{brand}"}
          aria-invalid={!!errors["appearance.title"]}
          onFocus={() => setTarget("title")}
          onChange={(e) => set({ title: e.target.value })}
        />
      </Field>
      <SwitchRow label={t("groups.appearance.noAnnounce")} sub={t("groups.appearance.noAnnounceSub")} checked={draft.noAnnounce} onChange={(noAnnounce) => set({ noAnnounce })} />
      {draft.noAnnounce ? null : (
        <>
          <Field label={t("settings.announce")} htmlFor={`${uid}-announce`} hint={t("settings.announceHint")} error={errors["appearance.announce"]}>
            <textarea
              id={`${uid}-announce`}
              className="input py-2"
              rows={3}
              value={draft.announce}
              maxLength={1000}
              placeholder={panel?.sub_announce || ""}
              aria-invalid={!!errors["appearance.announce"]}
              onFocus={() => setTarget("announce")}
              onChange={(e) => set({ announce: e.target.value })}
            />
          </Field>
          <Field label={t("settings.announceUrl")} htmlFor={`${uid}-announce-url`} hint={t("settings.announceUrlHint")} error={errors["appearance.announce_url"]}>
            <input
              id={`${uid}-announce-url`}
              className="input"
              value={draft.announceUrl}
              maxLength={200}
              placeholder={panel?.sub_announce_url || "https://t.me/your_channel"}
              aria-invalid={!!errors["appearance.announce_url"]}
              onChange={(e) => set({ announceUrl: e.target.value })}
            />
          </Field>
        </>
      )}
      <div className="-mt-1 mb-3 flex flex-wrap items-center gap-2" role="group" aria-label={t("settings.titleVars")}>
        <span className="text-xs text-[var(--ink-500)]">{t("settings.titleVars")}</span>
        {TITLE_VARS.map((v) => (
          <button key={v} type="button" className="chip-btn" title={t(`settings.titleVar.${v}`)} onClick={() => insert(v)}>
            <span className="mono">{`{${v}}`}</span>
          </button>
        ))}
      </div>
      {unknown.length > 0 ? (
        <p className="-mt-1 mb-3 text-xs font-medium text-[var(--honey-600)]">{t("settings.unknownVars", { vars: unknown.join(" ") })}</p>
      ) : null}
      <div className="grid gap-x-3 sm:grid-cols-[1fr_160px]">
        <Field label={t("groups.appearance.support")} htmlFor={`${uid}-support`} hint={t("groups.appearance.supportHint")} error={errors["appearance.support_url"]}>
          <input
            id={`${uid}-support`}
            className="input"
            value={draft.supportUrl}
            maxLength={200}
            placeholder={panel?.support_url || "https://t.me/support"}
            aria-invalid={!!errors["appearance.support_url"]}
            onChange={(e) => set({ supportUrl: e.target.value })}
          />
        </Field>
        <Field
          label={t("groups.appearance.interval")}
          htmlFor={`${uid}-hours`}
          hint={hours !== "" && updateHours(hours) === null ? t("groups.appearance.intervalBad") : t("groups.appearance.intervalHint")}
          error={errors["appearance.update_hours"]}
        >
          <input
            id={`${uid}-hours`}
            className="input num"
            inputMode="numeric"
            value={draft.updateHours}
            maxLength={3}
            placeholder="1"
            aria-invalid={!!errors["appearance.update_hours"] || (hours !== "" && updateHours(hours) === null)}
            onChange={(e) => set({ updateHours: e.target.value })}
          />
        </Field>
      </div>
    </>
  );
}
