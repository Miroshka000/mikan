import { useState } from "react";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";

/**
 * A limit: a few common values, none (∞) unless unlimited is false, and at the end
 * "Other", which opens a field for any number in [min, max]. A value that is not one of
 * the presets shows on the "Other" button itself. format names a value in words ("a week");
 * fieldUnit is what the typed number counts, when the buttons say it in words. inherit, when
 * given, is a first choice that leaves the limit to something else (a template's "as in the
 * tariff"): it stands for value undefined, and ∞ for null alone.
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
  inherit,
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
  inherit?: { label: string; onPick: () => void };
  busy?: boolean;
  onChange: (n: number | null, done: () => void) => void;
}) {
  const [typing, setTyping] = useState<string | null>(null);
  const own = value != null && !presets.includes(value);
  const n = Number(typing);
  const ok = typing !== null && typing.trim() !== "" && Number.isInteger(n) && n >= min && n <= max;
  const pick = (v: number | null) => onChange(v, () => setTyping(null));
  const none = inherit ? value === null : value == null;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        <span className="text-[var(--ink-600)]">{label}</span>
        {/* A narrow card (a drawer on a phone) wraps the choices rather than scroll past its edge. */}
        <div className="seg max-w-full flex-wrap" role="group" aria-label={label}>
          {inherit ? (
            <button
              type="button"
              aria-pressed={value === undefined && typing === null}
              disabled={busy}
              onClick={() => {
                setTyping(null);
                inherit.onPick();
              }}
            >
              {inherit.label}
            </button>
          ) : null}
          {presets.map((p) => (
            <button key={p} type="button" aria-pressed={value === p && typing === null} disabled={busy} onClick={() => pick(p)}>
              {format(p)}
            </button>
          ))}
          {unlimited ? (
            <button type="button" aria-pressed={none && typing === null} disabled={busy} onClick={() => pick(null)} aria-label={t("users.unlimited")}>
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
          <label className="input-unit max-w-[180px] flex-1">
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
          </label>
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
