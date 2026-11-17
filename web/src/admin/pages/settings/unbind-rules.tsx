import type { Schemas } from "../../../api/client";
import { t } from "../../../i18n";
import { period } from "../../../lib/format";
import { LimitPicker } from "../user/limit-picker";
import { TimesPerPeriod } from "./reissue-rules";

type Rules = Schemas["UnbindRules"];

/** A pause in hours: "at once", "6 h", whole days as days, "never" (-1: until the admin lets it). */
function pauseLabel(hours: number): string {
  if (hours === 0) return t("settings.unbindAtOnce");
  if (hours < 0) return t("settings.unbindNever");
  if (hours % 24 === 0) return t("settings.unbindDays", { n: hours / 24 });
  return t("settings.unbindHours", { n: hours });
}

/**
 * How subscribers unbind devices themselves: how many a period, and how long an unbound
 * device stays out, so that its app, still open, does not take the place back at once. The
 * panel's rules save as they change; a group's go with the group's form.
 */
export function UnbindRulesEditor({ value: r, busy, onChange }: { value: Rules; busy: boolean; onChange: (patch: Partial<Rules>, done: () => void) => void }) {
  const off = !!r.off;
  const summary = off
    ? t("settings.unbindSummaryOff")
    : r.limit === 0
      ? t("settings.unbindSummaryFree")
      : t("settings.unbindSummary", { n: r.limit, period: period(r.days) });
  const pause =
    r.return_hours < 0
      ? t("settings.unbindSummaryNever")
      : r.return_hours > 0
        ? t("settings.unbindSummaryPause", { pause: pauseLabel(r.return_hours) })
        : t("settings.unbindSummaryNoPause");
  return (
    <>
      <div className="flex flex-col gap-3">
        <TimesPerPeriod label={t("settings.unbindLimit")} value={r} busy={busy} onChange={onChange} />
        {off ? null : (
          <LimitPicker
            label={t("settings.unbindReturn")}
            value={r.return_hours}
            presets={[0, 6, 24, 72, -1]}
            min={0}
            max={720}
            unlimited={false}
            format={pauseLabel}
            fieldUnit={t("settings.unbindHoursUnit")}
            busy={busy}
            onChange={(n, done) => onChange({ return_hours: n ?? 0 }, done)}
          />
        )}
      </div>
      <p className="mt-3 text-xs text-[var(--ink-600)]" role="status">
        {off ? summary : `${summary} ${pause}`}
      </p>
    </>
  );
}
