import { useId, useState } from "react";
import type { Schemas } from "../../api/client";
import { useFolders, useTariffs, useUsers } from "../../api/hooks";
import { Button, Field } from "../../components/ui";
import { t } from "../../i18n";

type Rule = Schemas["Rule"];
type Scope = Rule["scope"] | "all";
type Audience = "users" | "folders" | "tariffs";
type Editable = Omit<Rule, "scope"> & { scope: Scope };

export function FilterRules({ rules, globalValues, onSave, onRemove, error }: {
  rules: Rule[];
  globalValues: Record<Rule["kind"], string[]>;
  onSave: (index: number, rule: Editable) => void;
  onRemove: (index: number) => void;
  error?: string;
}) {
  const [editing, setEditing] = useState<number | null>(null);
  return (
    <div className="mt-5 border-t border-[var(--line)] pt-4">
      <div className="mb-2 flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium">{t("filters.rules.title")}</h3>
        {editing === null && <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(-1)}>{t("filters.rules.add")}</Button>}
      </div>
      {error && <p className="banner err" role="alert">{error}</p>}
      {rules.length > 0 && (
        <ul className="mb-3 space-y-2">
          {rules.map((rule, i) => (
            <li key={i} className="panel-soft flex flex-wrap items-center gap-2 p-2">
              <span className="min-w-0 flex-1 break-all text-sm">
                {t(`filters.rules.${rule.kind}`)} <span className="mono">{rule.value}</span>
                <span className="ml-2 text-xs text-[var(--ink-600)]">{t(`filters.rules.${rule.scope}`)}</span>
              </span>
              <Button type="button" size="sm" variant="ghost" disabled={editing !== null} onClick={() => setEditing(i)}>{t("filters.rules.edit")}</Button>
              <Button type="button" size="sm" variant="ghost" disabled={editing !== null} onClick={() => onRemove(i)}>{t("filters.rules.remove")}</Button>
            </li>
          ))}
        </ul>
      )}
      {editing !== null && (
        <RuleEditor
          initial={rules[editing]}
          globalValues={globalValues}
          close={() => setEditing(null)}
          save={(rule) => {
            onSave(editing, rule);
            setEditing(null);
          }}
        />
      )}
    </div>
  );
}

function RuleEditor({ initial, globalValues, close, save }: {
  initial?: Rule;
  globalValues: Record<Rule["kind"], string[]>;
  close: () => void;
  save: (rule: Editable) => void;
}) {
  const id = useId();
  const [rule, setRule] = useState<Editable>(initial ?? { kind: "domain", value: "", scope: "all" });
  const hasAudience = Boolean(rule.users?.length || rule.folders?.length || rule.tariffs?.length);
  return (
    <div className="panel-soft p-3">
      <div className="grid gap-2 sm:grid-cols-2">
        <Field label={t("filters.rules.kind")} htmlFor={id + "-kind"}>
          <select id={id + "-kind"} className="input" value={rule.kind} onChange={(e) => setRule({ ...rule, kind: e.target.value as Rule["kind"] })}>
            {(["domain", "network", "port"] as const).map((kind) => <option key={kind} value={kind}>{t(`filters.rules.${kind}`)}</option>)}
          </select>
        </Field>
        <Field label={t("filters.rules.value")} htmlFor={id + "-value"}>
          <input id={id + "-value"} className="input mono" spellCheck={false} list={id + "-values"} value={rule.value} onChange={(e) => setRule({ ...rule, value: e.target.value })} />
          <datalist id={id + "-values"}>
            {[...new Set(globalValues[rule.kind])].map((value) => <option key={value} value={value} />)}
          </datalist>
        </Field>
      </div>
      <Field label={t("filters.rules.audience")} htmlFor={id + "-scope"}>
        <select id={id + "-scope"} className="input" value={rule.scope} onChange={(e) => setRule({ ...rule, scope: e.target.value as Scope })}>
          {(["all", "only", "except"] as const).map((scope) => <option key={scope} value={scope}>{t(`filters.rules.${scope}`)}</option>)}
        </select>
      </Field>
      {rule.scope !== "all" && <AudiencePickers rule={rule} set={(k, ids) => setRule({ ...rule, [k]: ids })} />}
      <div className="mt-3 flex gap-2">
        <Button type="button" size="sm" disabled={!rule.value.trim() || (rule.scope !== "all" && !hasAudience)} onClick={() => save({ ...rule, value: rule.value.trim() })}>
          {t("filters.rules.apply")}
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={close}>{t("filters.rules.cancel")}</Button>
      </div>
    </div>
  );
}

function AudiencePickers({ rule, set }: { rule: Editable; set: (key: Audience, ids: number[]) => void }) {
  const [q, setQ] = useState("");
  const folders = useFolders();
  const tariffs = useTariffs();
  const users = useUsers({ state: "all", q }, { limit: 50 });
  const id = useId();
  const options = {
    users: users.data?.items ?? [],
    folders: folders.data ?? [],
    tariffs: tariffs.data ?? [],
  };
  const failed = { users: users.isError, folders: folders.isError, tariffs: tariffs.isError };
  const pending = { users: users.isPending, folders: folders.isPending, tariffs: tariffs.isPending };
  const [labels, setLabels] = useState<Record<string, string>>({});

  return (
    <div className="space-y-2">
      <Field label={t("filters.rules.search")} htmlFor={id + "-search"}>
        <input id={id + "-search"} className="input" type="search" value={q} onChange={(e) => setQ(e.target.value)} />
      </Field>
      {(["users", "folders", "tariffs"] as const).map((key) => {
        const ids = rule[key] ?? [];
        const choices = options[key];
        const available = choices.filter((item) => !ids.includes(item.id));
        return (
          <Field key={key} label={t(`filters.rules.${key}`)} htmlFor={id + key} error={failed[key] ? t("filters.rules.loadError") : undefined}>
            <select id={id + key} className="input" value="" disabled={pending[key] || failed[key]} onChange={(e) => {
              const item = choices.find((choice) => choice.id === Number(e.target.value));
              if (!item) return;
              setLabels({ ...labels, [key + item.id]: item.name });
              set(key, [...ids, item.id]);
            }}>
              <option value="" disabled>{t("filters.rules.choose")}</option>
              {available.map((item) => <option key={item.id} value={item.id}>{item.name} · #{item.id}</option>)}
            </select>
            {ids.length > 0 && (
              <div className="mt-1 flex flex-wrap gap-1">
                {ids.map((picked) => {
                  const name = choices.find((item) => item.id === picked)?.name ?? labels[key + picked] ?? `#${picked}`;
                  return (
                    <Button key={picked} type="button" size="sm" variant="ghost" onClick={() => set(key, ids.filter((other) => picked !== other))} aria-label={t("filters.rules.removeName", { name })}>
                      {name} ×
                    </Button>
                  );
                })}
              </div>
            )}
          </Field>
        );
      })}
    </div>
  );
}
