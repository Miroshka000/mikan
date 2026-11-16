// The admin's own fields on users and nodes: the settings card that names them, the inputs a
// card fills them with, and the user's section. A value only describes: nothing a user may do
// or a node carries depends on it.
import { ArrowDown, ArrowUp, Pencil, Plus, Tags, Trash2 } from "lucide-react";
import { useState } from "react";
import { ApiError, errorText, type Schemas, type User } from "../../api/client";
import { customFieldActions, useCustomFieldMutation, useCustomFields, userActions, useUserMutation } from "../../api/hooks";
import { CardHead } from "../../components/layout";
import { Confirm, NameDialog } from "../../components/overlay";
import { QueryBoundary } from "../../components/query";
import { Select } from "../../components/select";
import { SwitchRow } from "../../components/switch";
import { useToast } from "../../components/toast";
import { Button, Field, Skeleton } from "../../components/ui";
import { t } from "../../i18n";
import { useDraft } from "../../lib/draft";
import { fieldErrors } from "../../lib/fields";
import { Section } from "./user/section";

export type CustomField = Schemas["CustomFieldView"];
type Scope = CustomField["scope"];
type Kind = CustomField["kind"];
const KINDS: Kind[] = ["text", "number", "bool", "date", "url"];
const SCOPES: Scope[] = ["user", "node"];

/** A key from a label: Latin letters, digits and _, from a letter; empty when nothing Latin is left. */
function keyFrom(label: string): string {
  const k = label
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^[^a-z]+/, "")
    .replace(/_+$/, "")
    .slice(0, 32);
  return k;
}

/** One field's input, in its kind; "" is no value. */
export function FieldInput({ field, id, value, onChange, onBlur, error }: { field: CustomField; id: string; value: string; onChange: (v: string) => void; onBlur?: () => void; error?: string }) {
  if (field.kind === "bool") {
    return <SwitchRow label={field.label} checked={value === "true"} onChange={(v) => onChange(v ? "true" : "")} />;
  }
  const type = field.kind === "number" ? "number" : field.kind === "date" ? "date" : field.kind === "url" ? "url" : "text";
  return (
    <Field label={field.label} htmlFor={id} error={error}>
      <input
        id={id}
        className={field.kind === "url" || field.kind === "number" ? "input mono" : "input"}
        type={type}
        inputMode={field.kind === "number" ? "decimal" : undefined}
        step={field.kind === "number" ? "any" : undefined}
        placeholder={field.kind === "url" ? "https://" : undefined}
        maxLength={500}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onBlur={onBlur}
        autoComplete="off"
        aria-invalid={!!error}
      />
    </Field>
  );
}

/** The values that differ from what the server has: a cleared one is sent as "". */
export function changedFields(draft: Record<string, string>, saved: Record<string, string>): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const k of new Set([...Object.keys(draft), ...Object.keys(saved)])) {
    const d = (draft[k] ?? "").trim();
    if (d !== (saved[k] ?? "")) out[k] = d;
  }
  return Object.keys(out).length ? out : undefined;
}

/** The errors of custom_fields.<key> by key. */
export function customFieldErrors(error: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(fieldErrors(error))) {
    if (k.startsWith("custom_fields.")) out[k.slice("custom_fields.".length)] = v;
  }
  return out;
}

/** The fields of a node's form, filled from the form's state; nothing without fields of nodes. */
export function NodeFieldsInputs({ values, onChange, errors }: { values: Record<string, string>; onChange: (key: string, v: string) => void; errors: Record<string, string> }) {
  const fields = useCustomFields();
  const list = (fields.data ?? []).filter((f) => f.scope === "node");
  if (list.length === 0) return null;
  return (
    <fieldset className="mt-2 border-t border-[var(--hairline)] pt-4">
      <legend className="mb-2 text-[13px] font-medium">{t("customFields.nodeTitle")}</legend>
      {list.map((f) => (
        <FieldInput key={f.id} field={f} id={`node-cf-${f.key}`} value={values[f.key] ?? ""} onChange={(v) => onChange(f.key, v)} error={errors[f.key]} />
      ))}
    </fieldset>
  );
}

/** A user's own fields on the card, saved with a button: a value being typed is kept while the card polls. */
export function UserFieldsSection({ u }: { u: User }) {
  const fields = useCustomFields();
  const toast = useToast();
  const update = useUserMutation(userActions.update);
  const { draft, setDraft, dirty, reset } = useDraft(u.custom_fields);
  const list = (fields.data ?? []).filter((f) => f.scope === "user");
  if (list.length === 0) return null;
  const errors = customFieldErrors(update.error);
  const save = () => {
    const custom_fields = changedFields(draft, u.custom_fields);
    if (!custom_fields) return;
    update.mutate({ id: u.id, body: { custom_fields } }, { onSuccess: () => toast.ok(t("customFields.saved")), onError: (e) => !(e instanceof ApiError && Object.keys(customFieldErrors(e)).length) && toast.error(errorText(e)) });
  };
  return (
    <Section title={t("customFields.userTitle")}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
        noValidate
      >
        {list.map((f) => (
          <FieldInput key={f.id} field={f} id={`user-cf-${f.key}`} value={draft[f.key] ?? ""} onChange={(v) => setDraft({ ...draft, [f.key]: v })} error={errors[f.key]} />
        ))}
        {dirty ? (
          <div className="mt-2 flex gap-2">
            <Button type="submit" size="sm" variant="primary" loading={update.isPending}>
              {t("common.save")}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => {
                reset();
                update.reset();
              }}
            >
              {t("common.discard")}
            </Button>
          </div>
        ) : null}
      </form>
    </Section>
  );
}

/** The settings card where the admin names the fields of users and of nodes. */
export function CustomFieldsCard() {
  const fields = useCustomFields();
  const toast = useToast();
  const create = useCustomFieldMutation(customFieldActions.create);
  const rename = useCustomFieldMutation(customFieldActions.rename);
  const remove = useCustomFieldMutation(customFieldActions.remove);
  const order = useCustomFieldMutation(customFieldActions.order);
  const [scope, setScope] = useState<Scope>("user");
  const [label, setLabel] = useState("");
  const [key, setKey] = useState<string | null>(null); // null: from the label
  const [kind, setKind] = useState<Kind>("text");
  const [renaming, setRenaming] = useState<CustomField | null>(null);
  const [deleting, setDeleting] = useState<CustomField | null>(null);
  const shownKey = key ?? keyFrom(label);
  const errors = fieldErrors(create.error);
  const fail = (e: unknown) => toast.error(errorText(e));
  const move = (list: CustomField[], i: number, by: number) => {
    const ids = list.map((f) => f.id);
    [ids[i], ids[i + by]] = [ids[i + by]!, ids[i]!];
    order.mutate({ scope: list[0]!.scope, ids }, { onError: fail });
  };
  return (
    <section className="card glass">
      <CardHead title={t("customFields.title")} sub={t("customFields.sub")} aside={<Tags size={20} className="text-[var(--ink-300)]" aria-hidden />} />
      <QueryBoundary query={fields} pending={<Skeleton style={{ height: 64 }} />}>
        {(all) => (
          <div className="space-y-4">
            {SCOPES.map((s) => {
              const list = all.filter((f) => f.scope === s);
              return (
                <div key={s}>
                  <div className="mb-1 text-[13px] font-medium">{t(`customFields.scope.${s}`)}</div>
                  {list.length === 0 ? (
                    <p className="text-xs text-[var(--ink-500)]">{t("customFields.none")}</p>
                  ) : (
                    <ul className="row-list">
                      {list.map((f, i) => (
                        <li key={f.id} className="flex items-center justify-between gap-3 py-2">
                          <div className="min-w-0">
                            <div className="truncate text-[13px] font-medium">{f.label}</div>
                            <div className="text-xs text-[var(--ink-500)]">
                              <span className="mono">{f.key}</span> · {t(`customFields.kind.${f.kind}`)} · {t("customFields.values", { n: f.values })}
                            </div>
                          </div>
                          <div className="flex shrink-0 gap-1">
                            <button type="button" className="icon-btn" aria-label={t("customFields.up")} disabled={i === 0 || order.isPending} onClick={() => move(list, i, -1)}>
                              <ArrowUp size={16} aria-hidden />
                            </button>
                            <button type="button" className="icon-btn" aria-label={t("customFields.down")} disabled={i === list.length - 1 || order.isPending} onClick={() => move(list, i, 1)}>
                              <ArrowDown size={16} aria-hidden />
                            </button>
                            <button type="button" className="icon-btn" aria-label={t("customFields.rename")} onClick={() => setRenaming(f)}>
                              <Pencil size={16} aria-hidden />
                            </button>
                            <button type="button" className="icon-btn" aria-label={t("common.delete")} onClick={() => setDeleting(f)}>
                              <Trash2 size={16} aria-hidden />
                            </button>
                          </div>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </QueryBoundary>

      <form
        className="mt-4 border-t border-[var(--hairline)] pt-4"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate(
            { scope, key: shownKey, label: label.trim(), kind },
            {
              onSuccess: (f) => {
                toast.ok(t("customFields.created", { label: f.label }));
                setLabel("");
                setKey(null);
                create.reset();
              },
              onError: (e) => !(e instanceof ApiError && Object.keys(e.fields).length) && fail(e),
            },
          );
        }}
        noValidate
      >
        <div className="grid gap-x-3 sm:grid-cols-2">
          <Field label={t("customFields.scopeLabel")} htmlFor="cf-scope">
            <Select id="cf-scope" className="input" value={scope} onChange={(e) => setScope(e.target.value as Scope)}>
              {SCOPES.map((s) => (
                <option key={s} value={s}>
                  {t(`customFields.scope.${s}`)}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("customFields.kindLabel")} htmlFor="cf-kind" hint={t("customFields.kindHint")}>
            <Select id="cf-kind" className="input" value={kind} onChange={(e) => setKind(e.target.value as Kind)}>
              {KINDS.map((k) => (
                <option key={k} value={k}>
                  {t(`customFields.kind.${k}`)}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("customFields.label")} htmlFor="cf-label" error={errors.label}>
            <input id="cf-label" className="input" maxLength={64} value={label} onChange={(e) => setLabel(e.target.value)} placeholder={t("customFields.labelPlaceholder")} autoComplete="off" aria-invalid={!!errors.label} />
          </Field>
          <Field label={t("customFields.key")} htmlFor="cf-key" hint={t("customFields.keyHint")} error={errors.key}>
            <input id="cf-key" className="input mono" maxLength={32} value={shownKey} onChange={(e) => setKey(e.target.value.toLowerCase())} placeholder="order_no" autoComplete="off" aria-invalid={!!errors.key} />
          </Field>
        </div>
        <Button type="submit" variant="glass" loading={create.isPending} disabled={!label.trim() || !/^[a-z][a-z0-9_]{0,31}$/.test(shownKey)}>
          <Plus size={16} aria-hidden /> {t("customFields.add")}
        </Button>
      </form>

      <NameDialog
        open={!!renaming}
        onOpenChange={(v) => {
          if (!v) {
            setRenaming(null);
            rename.reset();
          }
        }}
        title={t("customFields.renameTitle")}
        label={t("customFields.label")}
        initial={renaming?.label ?? ""}
        maxLength={64}
        error={fieldErrors(rename.error).label}
        loading={rename.isPending}
        onSubmit={(next) =>
          renaming &&
          rename.mutate(
            { id: renaming.id, label: next },
            {
              onSuccess: () => {
                setRenaming(null);
                rename.reset();
              },
              onError: (e) => !(e instanceof ApiError && e.fields.label) && fail(e),
            },
          )
        }
      />
      <Confirm
        open={!!deleting}
        onOpenChange={(v) => !v && setDeleting(null)}
        title={t("customFields.deleteTitle", { label: deleting?.label ?? "" })}
        text={deleting && deleting.values > 0 ? t("customFields.deleteText", { n: deleting.values }) : t("customFields.deleteEmpty")}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() =>
          deleting &&
          remove.mutate(deleting.id, {
            onSuccess: () => {
              setDeleting(null);
              toast.ok(t("customFields.deleted"));
            },
            onError: fail,
          })
        }
      />
    </section>
  );
}
