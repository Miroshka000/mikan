import type { Schemas } from "../../../api/client";
import { CardHead } from "../../../components/layout";
import { Pill, Segmented } from "../../../components/ui";
import { t } from "../../../i18n";
import { useSaveSettings } from "./shared";

const PRESETS = [0, 7, 14, 30, 90];

/**
 * How long the log of subscription requests is kept (a user's card shows it), or none at
 * all: the requests are then not written, and what was written goes at the next sweep.
 */
export function LogDaysCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const days = s.sub_log_days;
  // A value set through the API outside the presets is still a choice on screen.
  const options = (PRESETS.includes(days) ? PRESETS : [...PRESETS, days].sort((a, b) => a - b)).map((n) => ({ value: String(n), label: n === 0 ? t("settings.subLog.off") : t("settings.subLog.days", { n }) }));
  return (
    <section className="card glass reveal" style={{ "--i": 3 } as React.CSSProperties}>
      <CardHead title={t("settings.subLog.title")} sub={t("settings.subLog.sub")} aside={<Pill tone={days ? "ok" : "off"}>{days ? t("settings.subLog.days", { n: days }) : t("settings.subLog.off")}</Pill>} />
      <Segmented label={t("settings.subLog.title")} value={String(days)} options={options} disabled={save.isPending} onChange={(v) => save.mutate({ sub_log_days: Number(v) })} />
      <p className="mt-3 text-xs text-[var(--ink-500)]">{days === 0 ? t("settings.subLog.offHint") : t("settings.subLog.hint")}</p>
    </section>
  );
}
