import { Field } from "../../../components/ui";
import { t } from "../../../i18n";
import { batchRange, hasNumber } from "./model";

/** How many clients a batch makes at most, and the first number it may start from: the API's
 * limits (domain.MaxBatch, the body's maximum of start). */
export const MAX_BATCH = 500;
export const MAX_START = 999_999_999;

type Errors = Partial<Record<string, string>>;

/** A whole number typed in a field: anything but digits is dropped as it is typed. */
function DigitsField({ id, label, hint, error, value, onChange }: { id: string; label: string; hint?: string; error?: string; value: string; onChange: (v: string) => void }) {
  return (
    <Field label={label} htmlFor={id} hint={hint} error={error}>
      <input id={id} className="input" inputMode="numeric" value={value} onChange={(e) => onChange(e.target.value.replace(/[^\d]/g, ""))} aria-invalid={!!error} autoComplete="off" />
    </Field>
  );
}

/** The names of a batch: how many, from which number, the pattern, and the first and the last name it makes. */
export function BatchNameFields({
  count,
  start,
  pattern,
  errors,
  onCount,
  onStart,
  onPattern,
}: {
  count: string;
  start: string;
  pattern: string;
  errors: Errors;
  onCount: (v: string) => void;
  onStart: (v: string) => void;
  onPattern: (v: string) => void;
}) {
  const n = Number(count);
  const range = batchRange(pattern, Number(start), Math.max(n, 1));
  return (
    <>
      <div className="grid grid-cols-2 gap-x-3">
        <DigitsField id="nu-count" label={t("userCreate.count")} hint={t("userCreate.countHint", { max: MAX_BATCH })} error={errors.count} value={count} onChange={onCount} />
        <DigitsField id="nu-start" label={t("userCreate.start")} error={errors.start} value={start} onChange={onStart} />
      </div>
      <Field label={t("userCreate.pattern")} htmlFor="nu-pattern" hint={t("userCreate.patternHint")} error={errors.name_pattern}>
        <input
          id="nu-pattern"
          className="input mono"
          value={pattern}
          maxLength={100}
          onChange={(e) => onPattern(e.target.value)}
          aria-invalid={!!errors.name_pattern}
          autoComplete="off"
          spellCheck={false}
        />
      </Field>
      {hasNumber(pattern) ? (
        <p className="-mt-2 mb-4 text-xs text-[var(--ink-500)]" aria-live="polite">
          {n > 1 ? t("userCreate.batchPreview", range) : t("userCreate.batchPreviewOne", { first: range.first })}
        </p>
      ) : null}
    </>
  );
}
