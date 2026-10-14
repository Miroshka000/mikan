import { useId } from "react";
import { t } from "../../../i18n";

export type DeviceMode = "bound" | "online";

/**
 * How a device limit counts: the devices bound to the subscription (each takes a place),
 * or only the ones online at once (any number may bind; the nodes turn away the rest). The
 * hint under the buttons says what the chosen one means. With a label it sits in a row, as
 * the other limit pickers of the user card do; without, it fills a form field.
 */
export function DeviceModePicker({ value, onChange, label, busy }: { value: DeviceMode; onChange: (m: DeviceMode) => void; label?: string; busy?: boolean }) {
  const hintId = useId();
  const options: { value: DeviceMode; label: string }[] = [
    { value: "bound", label: t("deviceMode.bound") },
    { value: "online", label: t("deviceMode.online") },
  ];
  const group = (
    <div className="seg" role="group" aria-label={label ?? t("deviceMode.label")} aria-describedby={hintId}>
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === value} disabled={busy} onClick={() => o.value !== value && onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
  return (
    <div className="flex flex-col gap-2">
      {label ? (
        <div className="flex flex-wrap items-center gap-2 text-[13px]">
          <span className="text-[var(--ink-600)]">{label}</span>
          {group}
        </div>
      ) : (
        group
      )}
      <p id={hintId} className="text-xs text-[var(--ink-500)]">
        {t(value === "online" ? "deviceMode.onlineHint" : "deviceMode.boundHint")}
      </p>
    </div>
  );
}

/** A tariff's or user's mode, defaulting to "bound" for answers from a panel without modes. */
export function modeOf(x: { device_mode?: string }): DeviceMode {
  return x.device_mode === "online" ? "online" : "bound";
}
