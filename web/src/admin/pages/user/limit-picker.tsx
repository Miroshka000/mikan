import { useState } from "react";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";

/**
 * A limit: a few common values, none (∞) unless unlimited is false, and at the end
 * "Other", which opens a field for any number in [min, max]. A value that is not one of
 * the presets shows on the "Other" button itself. format names a value in words ("a week");
 * fieldUnit is what the typed number counts, when the buttons say it in words.
 */
export function LimitPicker({
  label,
  value,
  presets,
  min,
  max,
  unit,
  fieldUnit,
  format = String,
  unlimited = true,
  busy,
  onChange,
}: {
  label: string;
  value: number | null | undefined;
  presets: number[];
  min: number;
  max: number;
  unit?: string;
  fieldUnit?: string;
  format?: (n: number) => string;
  unlimited?: boolean;
  busy?: boolean;
  onChange: (n: number | null, done: () => void) => void;
}) {
  const [typing, setTyping] = useState<string | null>(null);
  const own = value != null && !presets.includes(value);
  const n = Number(typing);
  const ok = typing !== null && typing.trim() !== "" && Number.isInteger(n) && n >= min && n <= max;
  const pick = (v: number | null) => onChange(v, () => setTyping(null));
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        <span className="text-[var(--ink-600)]">{label}</span>
        <div className="seg" role="group" aria-label={label}>
          {presets.map((p) => (
            <button key={p} type="button" aria-pressed={value === p && typing === null} disabled={busy} onClick={() => pick(p)}>
              {format(p)}
            </button>
          ))}
          {unlimited ? (
            <button type="button" aria-pressed={value == null && typing === null} disabled={busy} onClick={() => pick(null)} aria-label={t("users.unlimited")}>
              ∞
            </button>
          ) : null}
          <button type="button" aria-pressed={own || typing !== null} aria-expanded={typing !== null} disabled={busy} onClick={() => setTyping(typing === null ? String(own ? value : "") : null)}>
            {own && typing === null ? format(value) : t("userDrawer.limitOther")}
          </button>
        </div>
        {unit ? <span className="text-xs text-[var(--ink-500)]">{unit}</span> : null}
      </div>
      {typing !== null ? (
        <form
          data-local-escape
          className="flex flex-wrap items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (ok) pick(n);
          }}
        >
          <span className="input-unit max-w-[180px] flex-1">
            <input
              className="input"
              inputMode="numeric"
              autoFocus
              value={typing}
              onChange={(e) => setTyping(e.target.value.replace(/[^\d]/g, ""))}
              onKeyDown={(e) => {
                // Escape closes the field; the drawer leaves it alone (data-local-escape).
                if (e.key === "Escape") setTyping(null);
              }}
              aria-label={label}
              aria-invalid={typing !== "" && !ok}
              placeholder={`${min}–${max}`}
            />
            {(fieldUnit ?? unit) ? <span>{fieldUnit ?? unit}</span> : null}
          </span>
          <Button size="sm" type="submit" disabled={!ok} loading={busy}>
            {t("userDrawer.limitSet")}
          </Button>
          <Button size="sm" variant="ghost" type="button" onClick={() => setTyping(null)}>
            {t("common.cancel")}
          </Button>
          {typing !== "" && !ok ? <span className="w-full text-xs text-[var(--berry-600)]">{t("userDrawer.limitRange", { min, max })}</span> : null}
        </form>
      ) : null}
    </div>
  );
}
