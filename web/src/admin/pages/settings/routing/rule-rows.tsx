import { AlertTriangle, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { Select } from "../../../../components/select";
import { SortableList } from "../../../../components/sortable";
import { Button } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { newComment, newRule, rowOf, rowsOf, RULE_TYPES, takesNoResolve, textOf, type RuleLine, type RuleRow } from "./rule-model";
import { RuleError, RuleExamples } from "./rules-text";

// As many rules as the server takes (routing.MaxRules); comments are not counted.
const MAX_RULES = 500;

/**
 * The admin's own rules as rows put in order by dragging: a rule is "type · value · where"
 * (and no-resolve for the types that resolve), a comment stays a row of its own. The text is
 * the draft: an edit here writes it again line for line, and a text changed elsewhere (the
 * text view, a discard) makes the rows again. badLine is the line a refusal names.
 */
export function RuleRows({ value, onChange, error, badLine, targets }: { value: string; onChange: (f: (v: string) => string) => void; error?: string; badLine: number; targets: string[] }) {
  // The rows of the text and the line of each, made again only when the text is not theirs:
  // React renders again with them before anything is shown.
  const [made, setMade] = useState(() => ({ text: value, ...rowsOf(value) }));
  if (made.text !== value) setMade({ text: value, ...rowsOf(value) });
  const { rows, lines } = made;
  const byKey = new Map(rows.map((r) => [r.key, r]));
  const commit = (next: RuleRow[]) => {
    const text = textOf(next);
    setMade({ text, rows: next, lines: next.map((_, i) => i + 1) });
    onChange(() => text);
  };
  const set = (row: RuleRow) => commit(rows.map((x) => (x.key === row.key ? row : x)));
  const rules = rows.filter((r) => r.kind === "rule").length;
  const main = targets.includes("PROXY") ? "PROXY" : (targets[0] ?? "DIRECT");
  return (
    <>
      {rows.length ? (
        <SortableList values={rows.map((r) => r.key)} onReorder={(keys) => commit(keys.map((k) => byKey.get(k)!))} label={t("settings.rules")} nameOf={(k) => nameOf(byKey.get(k)!)}>
          {(k, i) => <RowFields row={byKey.get(k)!} bad={lines[i] === badLine} targets={targets} onChange={set} onRemove={() => commit(rows.filter((x) => x.key !== k))} />}
        </SortableList>
      ) : (
        <p className="text-[13px] text-[var(--ink-500)]">{t("settings.rulesRows.empty")}</p>
      )}
      <div className="mt-3 flex flex-wrap gap-2">
        <Button variant="ghost" size="sm" disabled={rules >= MAX_RULES} onClick={() => commit([...rows, newRule(main)])}>
          <Plus size={16} aria-hidden /> {t("settings.rulesRows.addRule")}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => commit([...rows, newComment()])}>
          <Plus size={16} aria-hidden /> {t("settings.rulesRows.addComment")}
        </Button>
      </div>
      <RuleError error={error} />
      <RuleExamples onAdd={(rule) => rules < MAX_RULES && commit([...rows, rowOf(rule)])} />
    </>
  );
}

/** A row's name for screen readers: what tells it from the others. */
function nameOf(row: RuleRow): string {
  return row.kind === "rule" ? `${row.type} ${row.value || "…"} → ${row.target}` : row.text;
}

function RowFields({ row, bad, targets, onChange, onRemove }: { row: RuleRow; bad: boolean; targets: string[]; onChange: (r: RuleRow) => void; onRemove: () => void }) {
  const remove = (
    <button type="button" className="icon-btn shrink-0" aria-label={t("settings.rulesRows.remove", { name: nameOf(row) })} title={t("common.delete")} onClick={onRemove}>
      <Trash2 size={16} aria-hidden />
    </button>
  );
  if (row.kind !== "rule") {
    return (
      <div className="flex min-w-0 items-center gap-2">
        {row.kind === "raw" ? <AlertTriangle size={16} className="shrink-0 text-[var(--honey-600)]" aria-label={t("settings.rulesRows.raw")} /> : null}
        <input
          className="input mono h-9 min-w-0 flex-1 text-[13px]"
          value={row.text}
          aria-invalid={bad || undefined}
          aria-label={row.kind === "comment" ? t("settings.rulesRows.comment") : t("settings.rulesRows.raw")}
          title={row.kind === "raw" ? t("settings.rulesRows.raw") : undefined}
          spellCheck={false}
          maxLength={512}
          style={row.kind === "comment" ? { color: "var(--ink-500)" } : undefined}
          onChange={(e) => onChange({ ...row, text: e.target.value })}
          // A line typed into a rule becomes the rule's row once the admin leaves it.
          onBlur={() => row.kind === "raw" && onChange({ ...rowOf(row.text), key: row.key })}
        />
        {remove}
      </div>
    );
  }
  const edit = (patch: Partial<RuleLine>) => onChange({ ...row, ...patch });
  // A target saved before that is no target now (a group renamed) is still shown as it is.
  const shownTargets = targets.includes(row.target) ? targets : [...targets, row.target];
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2">
      <Select className="input h-9 w-[160px] text-[13px]" value={row.type} onChange={(e) => edit({ type: e.target.value })} aria-label={t("settings.rulesRows.type")}>
        {RULE_TYPES.map((x) => (
          <option key={x} value={x}>
            {x}
          </option>
        ))}
      </Select>
      <input
        className="input mono h-9 min-w-[140px] flex-1 text-[13px]"
        value={row.value}
        aria-invalid={bad || undefined}
        aria-label={t("settings.rulesRows.value")}
        placeholder="example.com"
        spellCheck={false}
        autoCapitalize="off"
        maxLength={480}
        onChange={(e) => edit({ value: e.target.value })}
      />
      <Select className="input h-9 w-[150px] text-[13px]" value={row.target} onChange={(e) => edit({ target: e.target.value })} aria-label={t("settings.rulesRows.target")}>
        {shownTargets.map((x) => (
          <option key={x} value={x}>
            {x}
          </option>
        ))}
      </Select>
      {takesNoResolve(row.type) ? (
        <label className="flex items-center gap-2 text-xs text-[var(--ink-700)]" title={t("settings.rulesRows.noResolveHint")}>
          <input type="checkbox" className="check" checked={row.noResolve} onChange={(e) => edit({ noResolve: e.target.checked })} />
          no-resolve
        </label>
      ) : null}
      {remove}
    </div>
  );
}
