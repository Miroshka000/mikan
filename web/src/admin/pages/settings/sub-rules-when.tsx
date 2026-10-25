import { Plus, X } from "lucide-react";
import type { Schemas } from "../../../api/client";
import { Select } from "../../../components/select";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";

type Cond = Schemas["Cond"];
type Group = Schemas["Group"];
type Inner = Schemas["Inner"];
type Limits = Schemas["SubRuleLimits"];

const OPS = ["equals", "contains", "regex"] as const;
const HEADERS = ["User-Agent", "X-Device-Os", "X-Hwid", "X-Device-Model", "X-Ver-Os", "Accept"] as const;

export const HEADER_LIST = "sub-rule-headers";

/** The headers a condition is most often about, offered by the header fields (they take any name). */
export function HeaderNames() {
  return (
    <datalist id={HEADER_LIST}>
      {HEADERS.map((h) => (
        <option key={h} value={h} />
      ))}
    </datalist>
  );
}

export const blankCond = (): Cond => ({ header: "User-Agent", op: "contains", value: "", not: false });

const placeholder = (op: Cond["op"]) => (op === "regex" ? "^Happ/\\d" : op === "equals" ? "ios" : "happ");

/**
 * When a rule applies: conditions on the headers of the request, joined by "all" or "any",
 * and groups of conditions inside that join their own the same way: (A or B) and C is the
 * conditions "all of" C and a group "any of" A and B. Without any, the rule applies to every request.
 */
export function WhenEditor({ when, onChange, errors, at, limits }: { when: Group; onChange: (g: Group) => void; errors: Record<string, string>; at: string; limits: Limits }) {
  const total = when.conds.length + when.groups.reduce((n, g) => n + g.conds.length, 0);
  const full = total >= limits.conds;
  const setGroup = (i: number, g: Inner) => onChange({ ...when, groups: when.groups.map((x, j) => (j === i ? g : x)) });
  const empty = when.conds.length + when.groups.length === 0;
  return (
    <div className="flex flex-col gap-3">
      <GroupBox
        id={`${at}-top`}
        at={at}
        op={when.op}
        conds={when.conds}
        errors={errors}
        limits={limits}
        full={full}
        onOp={(op) => onChange({ ...when, op })}
        onConds={(conds) => onChange({ ...when, conds })}
      />
      {when.groups.map((g, i) => (
        <GroupBox
          key={i}
          id={`${at}-g${i}`}
          at={`${at}.groups[${i}]`}
          nested
          op={g.op}
          conds={g.conds}
          errors={errors}
          limits={limits}
          full={full}
          onOp={(op) => setGroup(i, { ...g, op })}
          onConds={(conds) => setGroup(i, { ...g, conds })}
          onRemove={() => onChange({ ...when, groups: when.groups.filter((_, j) => j !== i) })}
        />
      ))}
      {errors[`${at}.groups`] ? (
        <p className="text-xs text-[var(--berry-600)]" role="alert">
          {errors[`${at}.groups`]}
        </p>
      ) : null}
      {errors[at] ? (
        <p className="text-xs text-[var(--berry-600)]" role="alert">
          {errors[at]}
        </p>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" disabled={full} onClick={() => onChange({ ...when, groups: [...when.groups, { op: "or", conds: [blankCond()] }] })} aria-label={t("settings.subRules.addGroup")} title={when.groups.length >= limits.groups ? undefined : t("settings.subRules.addGroupHint")}>
          <Plus size={16} aria-hidden /> {t("settings.subRules.addGroup")}
        </Button>
        {empty ? <span className="text-xs text-[var(--ink-500)]">{t("settings.subRules.everyRequestNote")}</span> : null}
      </div>
    </div>
  );
}

function GroupBox({
  id,
  at,
  nested,
  op,
  conds,
  errors,
  limits,
  full,
  onOp,
  onConds,
  onRemove,
}: {
  id: string;
  at: string;
  nested?: boolean;
  op: Group["op"];
  conds: Cond[];
  errors: Record<string, string>;
  limits: Limits;
  full: boolean;
  onOp: (op: Group["op"]) => void;
  onConds: (c: Cond[]) => void;
  onRemove?: () => void;
}) {
  const setCond = (i: number, patch: Partial<Cond>) => onConds(conds.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  const body = (
    <>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <label htmlFor={`${id}-op`} className="basis-full text-[13px] font-medium text-[var(--ink-600)] sm:basis-auto">
          {nested ? t("settings.subRules.groupOf") : t("settings.subRules.conditions")}
        </label>
        <Select id={`${id}-op`} className="input min-w-[160px] flex-1 sm:max-w-[280px]" value={op} onChange={(e) => onOp(e.target.value as Group["op"])}>
          <option value="and">{t("settings.subRules.opAnd")}</option>
          <option value="or">{t("settings.subRules.opOr")}</option>
        </Select>
        {onRemove ? (
          <Button size="sm" variant="ghost" className="ml-auto shrink-0" aria-label={t("settings.subRules.removeGroup")} title={t("settings.subRules.removeGroup")} onClick={onRemove}>
            <X size={16} aria-hidden />
          </Button>
        ) : null}
      </div>
      <ul className="flex flex-col gap-2">
        {conds.map((c, i) => {
          const where = `${at}.conds[${i}]`;
          const error = errors[`${where}.value`] ?? errors[`${where}.header`] ?? errors[`${where}.op`] ?? errors[where];
          return (
            // A phone gets the header on the first line and the comparison and the value under it; wider, one line.
            <li key={i} className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 sm:grid-cols-[auto_148px_136px_minmax(0,1fr)_auto]">
              <button type="button" className="chip-btn" aria-pressed={!!c.not} title={t("settings.subRules.notHint")} onClick={() => setCond(i, { not: !c.not })}>
                {t("settings.subRules.not")}
              </button>
              <input className="input mono min-w-0" list={HEADER_LIST} value={c.header} maxLength={64} spellCheck={false} autoCapitalize="off" aria-label={t("settings.subRules.header")} aria-invalid={!!errors[`${where}.header`]} onChange={(e) => setCond(i, { header: e.target.value })} />
              <Select className="input col-span-3 sm:col-span-1 sm:col-start-3 sm:row-start-1" value={c.op} aria-label={t("settings.subRules.compare")} onChange={(e) => setCond(i, { op: e.target.value as Cond["op"] })}>
                {OPS.map((o) => (
                  <option key={o} value={o}>
                    {t(`settings.subRules.op.${o}`)}
                  </option>
                ))}
              </Select>
              <input
                className="input mono col-span-3 min-w-0 sm:col-span-1 sm:col-start-4 sm:row-start-1"
                value={c.value}
                maxLength={limits.value}
                spellCheck={false}
                autoCapitalize="off"
                autoComplete="off"
                placeholder={placeholder(c.op)}
                aria-label={t("settings.subRules.value")}
                aria-invalid={!!errors[`${where}.value`]}
                onChange={(e) => setCond(i, { value: e.target.value })}
              />
              <Button size="sm" variant="ghost" className="col-start-3 row-start-1 sm:col-start-5" aria-label={t("settings.subRules.removeCond")} title={t("settings.subRules.removeCond")} disabled={nested && conds.length === 1} onClick={() => onConds(conds.filter((_, j) => j !== i))}>
                <X size={16} aria-hidden />
              </Button>
              {error ? (
                <p className="col-span-full text-xs text-[var(--berry-600)]" role="alert">
                  {error}
                </p>
              ) : null}
            </li>
          );
        })}
      </ul>
      <div>
        <Button size="sm" disabled={full} onClick={() => onConds([...conds, blankCond()])}>
          <Plus size={16} aria-hidden /> {t("settings.subRules.addCond")}
        </Button>
        {full ? <span className="ml-2 text-xs text-[var(--ink-500)]">{t("settings.subRules.condsFull", { n: limits.conds })}</span> : null}
      </div>
    </>
  );
  return nested ? <div className="panel-soft flex flex-col gap-3 p-3">{body}</div> : <div className="flex flex-col gap-3">{body}</div>;
}
