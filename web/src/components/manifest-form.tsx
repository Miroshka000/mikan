// The settings form of an addon, drawn from what the addon's manifest says it needs
// (docs/pipeline/addons.md, section 3). The server checks every value again; this only
// gives each type the right control and puts a refusal under its field.
import type { CSSProperties } from "react";
import type { Schemas } from "../api/client";
import { t } from "../i18n";
import { localized } from "../lib/localized";
import { Field } from "./ui";

type ManifestField = Schemas["AddonField"];

/** What the form holds per setting key: a flag for bool, the typed text for the rest. */
export type ManifestValues = Record<string, string | boolean>;

/** The form as the saved settings fill it in. Secrets start empty: the API never sends them. */
export function manifestInitial(fields: ManifestField[]): ManifestValues {
  const v: ManifestValues = {};
  for (const f of fields) {
    if (f.unsupported) continue;
    if (f.type === "bool") v[f.key] = f.value === true;
    else if (f.secret) v[f.key] = "";
    else if (typeof f.value === "string") v[f.key] = f.value;
    else if (typeof f.value === "number") v[f.key] = String(f.value);
    else v[f.key] = "";
  }
  return v;
}

/**
 * The settings of a PATCH. An empty secret is left out (the saved one stays), an emptied
 * number is null (cleared), a field of a type this panel does not know is never sent.
 * Code keeps its whitespace; the other text is trimmed.
 */
export function manifestPayload(fields: ManifestField[], values: ManifestValues): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const f of fields) {
    if (f.unsupported) continue;
    const v = values[f.key];
    if (typeof v !== "string") {
      out[f.key] = v;
      continue;
    }
    if (f.secret && v === "") continue;
    if (f.type === "number") out[f.key] = v.trim() === "" ? null : Number(v);
    else out[f.key] = f.type === "code" ? v : v.trim();
  }
  return out;
}

/** The placeholder of an empty field: the saved secret's note, else the addon's default. */
function placeholder(f: ManifestField): string {
  if (f.secret && f.set) return t("payments.keySaved");
  if (f.secret || f.default === undefined || f.default === null) return "";
  return String(f.default);
}

const WIDE: CSSProperties = { gridColumn: "1 / -1" };

export function ManifestForm({
  idPrefix,
  fields,
  values,
  onChange,
  errors,
  disabled,
}: {
  idPrefix: string;
  fields: ManifestField[];
  values: ManifestValues;
  onChange: (key: string, value: string | boolean) => void;
  /** The API's field errors, `settings.<key>` to text. */
  errors: Record<string, string>;
  disabled?: boolean;
}) {
  return (
    <div className="grid gap-x-3 sm:grid-cols-2">
      {fields.map((f) => (
        <FieldControl key={f.key} id={`${idPrefix}-${f.key}`} f={f} value={values[f.key]} onChange={(v) => onChange(f.key, v)} error={errors[`settings.${f.key}`]} disabled={disabled} />
      ))}
    </div>
  );
}

function FieldControl({ id, f, value, onChange, error, disabled }: { id: string; f: ManifestField; value: string | boolean | undefined; onChange: (v: string | boolean) => void; error?: string; disabled?: boolean }) {
  const label = localized(f.label, f.key);
  const help = localized(f.help, "");
  const text = typeof value === "string" ? value : "";

  if (f.unsupported)
    return (
      <div className="banner warn mb-4" style={WIDE} role="status">
        <span className="min-w-0 flex-1 break-words">{t("addons.form.unsupported", { label, type: f.type })}</span>
      </div>
    );

  if (f.type === "bool") {
    const noteId = error || help ? `${id}-note` : undefined;
    return (
      <div className="mb-4" style={WIDE}>
        <label className="flex items-center gap-2 text-[13px]">
          <input type="checkbox" id={id} className="check" checked={value === true} onChange={(e) => onChange(e.target.checked)} disabled={disabled} aria-invalid={!!error} aria-describedby={noteId} /> {label}
        </label>
        {error ? (
          <div className="mt-1 text-xs text-[var(--berry-600)]" role="alert" id={noteId}>
            {error}
          </div>
        ) : help ? (
          <div className="mt-1 text-xs text-[var(--ink-500)]" id={noteId}>
            {help}
          </div>
        ) : null}
      </div>
    );
  }

  const common = { id, value: text, disabled, "aria-invalid": !!error, "aria-required": f.required || undefined, placeholder: placeholder(f) };

  if (f.type === "select") {
    const options = f.options ?? [];
    const byDefault = options.find((o) => o.value === f.default);
    return (
      <Field label={label} htmlFor={id} hint={help || undefined} error={error}>
        <select id={id} className="input" value={text} disabled={disabled} aria-invalid={!!error} aria-required={f.required || undefined} onChange={(e) => onChange(e.target.value)}>
          <option value="">{byDefault ? t("addons.form.byDefault", { value: localized(byDefault.label, byDefault.value) }) : t("addons.form.notChosen")}</option>
          {options.map((o) => (
            <option key={o.value} value={o.value}>
              {localized(o.label, o.value)}
            </option>
          ))}
        </select>
      </Field>
    );
  }

  if (f.type === "number")
    return (
      <Field label={label} htmlFor={id} hint={help || rangeHint(f)} error={error}>
        <input {...common} className="input mono" type="number" inputMode="numeric" step={1} min={f.min} max={f.max} onChange={(e) => onChange(e.target.value)} autoComplete="off" />
      </Field>
    );

  if (f.type === "text" || f.type === "code") {
    const code = f.type === "code";
    return (
      <div style={WIDE}>
        <Field label={label} htmlFor={id} hint={help || (code ? t("addons.form.codeHint") : undefined)} error={error}>
          <textarea
            {...common}
            className={code ? "input mono min-h-[240px] overflow-auto whitespace-pre [tab-size:2]" : f.secret ? "input min-h-[96px] [-webkit-text-security:disc]" : "input min-h-[96px]"}
            wrap={code ? "off" : undefined}
            maxLength={code ? 65536 : 4096}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            autoComplete="off"
            onChange={(e) => onChange(e.target.value)}
          />
        </Field>
      </div>
    );
  }

  return (
    <Field label={label} htmlFor={id} hint={help || undefined} error={error}>
      <input
        {...common}
        className="input mono"
        type={f.secret ? "password" : "text"}
        maxLength={4096}
        autoComplete={f.secret ? "new-password" : "off"}
        onChange={(e) => onChange(e.target.value)}
      />
    </Field>
  );
}

/** "1 to 10" under a number field that has bounds and no help text of its own. */
function rangeHint(f: ManifestField): string | undefined {
  if (f.min !== undefined && f.max !== undefined) return t("addons.form.range", { min: f.min, max: f.max });
  if (f.min !== undefined) return t("addons.form.atLeast", { min: f.min });
  if (f.max !== undefined) return t("addons.form.atMost", { max: f.max });
  return undefined;
}
