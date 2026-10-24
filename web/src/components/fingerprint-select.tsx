// The TLS fingerprint picker: the profiles every app knows, or an own one typed in.
import { useEffect, useRef, useState } from "react";
import { t } from "../i18n";
import { FINGERPRINTS, fingerprintLabel, isKnownFingerprint, validFingerprint } from "../lib/fingerprints";
import { Select } from "./select";

const OWN = "__own";

/**
 * value "" means the inherited default when defaultLabel is given. onValid reports
 * whether an own value has the right shape, so the form can refuse to save.
 */
export function FingerprintSelect({
  id,
  value,
  onChange,
  defaultLabel,
  disabled,
  invalid,
  onValid,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  defaultLabel?: string;
  disabled?: boolean;
  invalid?: boolean;
  onValid?: (ok: boolean) => void;
}) {
  const own = value !== "" && !isKnownFingerprint(value);
  const [ownMode, setOwnMode] = useState(own);
  // An unknown value from outside opens the own field; only the list closes it, so typing
  // "chrome120" does not snap back to Chrome halfway.
  useEffect(() => {
    if (value !== "" && !isKnownFingerprint(value)) setOwnMode(true);
  }, [value]);
  const bad = ownMode && !validFingerprint(value);
  // The picker leaving the screen (another tab of the form) must not leave its verdict
  // behind: a field that is not shown cannot block saving.
  // The parent may hand a new callback on every render: the verdict follows bad alone.
  const tell = useRef(onValid);
  useEffect(() => {
    tell.current = onValid;
  });
  useEffect(() => {
    tell.current?.(!bad);
    return () => tell.current?.(true);
  }, [bad]);
  return (
    <div>
      <Select
        id={id}
        className="input max-w-[320px]"
        value={ownMode ? OWN : value}
        onChange={(e) => {
          if (e.target.value === OWN) {
            setOwnMode(true);
            onChange("");
            return;
          }
          setOwnMode(false);
          onChange(e.target.value);
        }}
        aria-invalid={invalid || bad}
        disabled={disabled}
      >
        {defaultLabel !== undefined ? <option value="">{defaultLabel}</option> : null}
        {FINGERPRINTS.map((fp) => (
          <option key={fp} value={fp}>
            {fingerprintLabel(fp)}
          </option>
        ))}
        <option value={OWN}>{t("settings.fpOwn")}</option>
      </Select>
      {ownMode ? (
        <div className="mt-2">
          <input
            className="input mono max-w-[320px]"
            value={value}
            onChange={(e) => onChange(e.target.value.trim())}
            placeholder="chrome120"
            maxLength={32}
            spellCheck={false}
            autoComplete="off"
            aria-label={t("settings.fpOwn")}
            aria-invalid={bad}
            disabled={disabled}
          />
          <p className={`mt-1 text-xs ${bad && value ? "text-[var(--berry-600)]" : "text-[var(--ink-500)]"}`} role={bad && value ? "alert" : undefined}>
            {bad && value ? t("settings.fpOwnBad") : t("settings.fpOwnHint")}
          </p>
        </div>
      ) : null}
    </div>
  );
}
