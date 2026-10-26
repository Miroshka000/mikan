import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { ChevronDown, ChevronUp, Plus, Trash2, X } from "lucide-react";
import { useState } from "react";
import { api, unwrap, type Schemas } from "../../../api/client";
import { qk, useSubRules, useSubTemplates } from "../../../api/hooks";
import { Card, SaveBar } from "../../../components/layout";
import { Confirm } from "../../../components/overlay";
import { QueryBoundary } from "../../../components/query";
import { Select } from "../../../components/select";
import { Switch, SwitchRow } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button, Field, Pill, Skeleton } from "../../../components/ui";
import { t, tMaybe } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors, onFormError } from "../../../lib/fields";
import { cleanRule } from "../../../lib/sub-rules";
import { RuleTester } from "./sub-rules-test";
import { CORES, coreName } from "./sub-templates";
import { HeaderNames, WhenEditor, blankCond } from "./sub-rules-when";

type Rule = Schemas["Rule"];
type Action = Schemas["Action"];
type Group = Schemas["Group"];
type Limits = Schemas["SubRuleLimits"];
type Draft = { rules: Rule[]; builtin: boolean };

const RESPONDS = ["serve", "stub", "403", "404", "html", "drop"] as const;

/**
 * What the subscription answers a request by its headers, before anything is made for it:
 * rules in order, the first that matches decides, then the built-in protection and the list
 * of blocked apps. The whole list is one draft, saved at once; the tester runs the draft.
 */
export function SubResponses() {
  const q = useSubRules();
  return (
    <QueryBoundary query={q} pending={<Skeleton style={{ height: 320, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
      {(v) => <ResponsesForm v={v} />}
    </QueryBoundary>
  );
}

const example = (name: string, when: Group, action: Action): Rule => ({ name, enabled: true, when, do: action });

// Rules to start from, one tap each: the admin changes the words and the values.
const EXAMPLES: { key: "scrapers" | "browsers" | "tv"; rule: () => Rule }[] = [
  {
    key: "scrapers",
    rule: () => example(t("settings.subRules.ex.scrapers"), { op: "or", conds: [{ header: "User-Agent", op: "regex", value: "(?i)curl|wget|python-requests|go-http-client", not: false }], groups: [] }, { respond: "403" }),
  },
  {
    key: "browsers",
    rule: () => example(t("settings.subRules.ex.browsers"), { op: "and", conds: [{ header: "User-Agent", op: "contains", value: "mozilla", not: false }], groups: [] }, { respond: "html", html: t("settings.subRules.ex.browsersHtml") }),
  },
  {
    key: "tv",
    rule: () => example(t("settings.subRules.ex.tv"), { op: "and", conds: [{ header: "User-Agent", op: "contains", value: "tv", not: false }], groups: [] }, { respond: "serve", skip_hwid: true }),
  },
];

function ResponsesForm({ v }: { v: Schemas["SubRulesView"] }) {
  const qc = useQueryClient();
  const toast = useToast();
  const { draft, setDraft, dirty, reset } = useDraft<Draft>({ rules: v.rules, builtin: v.builtin });
  const [open, setOpen] = useState<number | null>(null);
  // The place (from 1) of the rule the tester found; any edit makes it stale.
  const [fired, setFired] = useState(0);
  const save = useMutation({
    mutationFn: (d: Draft) => unwrap(api.PUT("/api/v1/sub-rules", { body: { rules: d.rules.map(cleanRule), builtin: d.builtin } })),
    onSuccess: (next) => {
      qc.setQueryData(qk.subRules, next);
      setDraft({ rules: next.rules, builtin: next.builtin });
      toast.ok(t("settings.subRules.saved"));
    },
    // The rule a refusal is about opens, so that the message is in sight.
    onError: (e) =>
      onFormError(e, toast, (fields) => {
        const at = Object.keys(fields)
          .map((k) => /^rules\[(\d+)\]/.exec(k)?.[1])
          .find((i) => i !== undefined);
        if (at !== undefined) setOpen(Number(at));
      }),
  });
  const errors = fieldErrors(save.error);
  const leave = useBlocker({
    shouldBlockFn: ({ current, next }) => {
      const s = next.search as { tab?: string; part?: string };
      return dirty && (current.pathname !== next.pathname || s.tab !== "subscription" || s.part !== "responses");
    },
    enableBeforeUnload: () => dirty,
    withResolver: true,
  });
  const edit = (f: (d: Draft) => Draft) => {
    setDraft(f);
    setFired(0);
  };
  const setRule = (i: number, rule: Rule) => edit((d) => ({ ...d, rules: d.rules.map((r, j) => (j === i ? rule : r)) }));
  const move = (i: number, by: -1 | 1) => {
    edit((d) => {
      const rules = [...d.rules];
      [rules[i], rules[i + by]] = [rules[i + by]!, rules[i]!];
      return { ...d, rules };
    });
    setOpen((o) => (o === i ? i + by : o === i + by ? i : o));
  };
  const remove = (i: number) => {
    edit((d) => ({ ...d, rules: d.rules.filter((_, j) => j !== i) }));
    setOpen(null);
  };
  const add = (rule: Rule) => {
    edit((d) => ({ ...d, rules: [...d.rules, rule] }));
    setOpen(draft.rules.length);
  };
  const full = draft.rules.length >= v.limits.rules;
  const blank = () => example(t("settings.subRules.newName", { n: draft.rules.length + 1 }), { op: "and", conds: [blankCond()], groups: [] }, { respond: "serve" });
  return (
    <div className="flex flex-col gap-4">
      <HeaderNames />
      <Card
        title={t("settings.subRules.title")}
        sub={t("settings.subRules.sub")}
        aside={
          <Button size="sm" disabled={full} onClick={() => add(blank())}>
            <Plus size={16} aria-hidden /> {t("settings.subRules.add")}
          </Button>
        }
      >
        {draft.rules.length === 0 ? (
          <div className="panel-soft p-4">
            <p className="text-[13px] text-[var(--ink-600)]">{t("settings.subRules.empty")}</p>
            <div className="mt-3 flex flex-wrap gap-2" role="group" aria-label={t("settings.subRules.examples")}>
              {EXAMPLES.map((x) => (
                <button key={x.key} type="button" className="chip-btn" onClick={() => add(x.rule())}>
                  + {t(`settings.subRules.ex.${x.key}`)}
                </button>
              ))}
            </div>
          </div>
        ) : (
          <ol className="flex flex-col gap-3">
            {draft.rules.map((r, i) => (
              <RuleCard
                key={i}
                i={i}
                n={draft.rules.length}
                rule={r}
                open={open === i}
                fires={fired === i + 1}
                errors={errors}
                limits={v.limits}
                formats={v.formats}
                onToggle={() => setOpen(open === i ? null : i)}
                onChange={(rule) => setRule(i, rule)}
                onMove={(by) => move(i, by)}
                onRemove={() => remove(i)}
              />
            ))}
          </ol>
        )}
        {full ? <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.subRules.full", { n: v.limits.rules })}</p> : null}
        <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.subRules.order")}</p>
      </Card>
      <Card title={t("settings.subRules.builtinTitle")} sub={t("settings.subRules.builtinSub")} i={2}>
        <SwitchRow label={t("settings.subRules.builtinOn")} sub={draft.builtin ? t("settings.subRules.builtinOnSub") : t("settings.subRules.builtinOffSub")} checked={draft.builtin} onChange={(builtin) => edit((d) => ({ ...d, builtin }))} />
      </Card>
      <RuleTester rules={draft.rules} builtin={draft.builtin} onResult={setFired} />
      <SaveBar
        dirty={dirty}
        saving={save.isPending}
        onSave={() => save.mutate(draft)}
        onReset={() => {
          reset();
          setOpen(null);
          setFired(0);
        }}
      />
      <Confirm open={leave.status === "blocked"} onOpenChange={(o) => !o && leave.reset?.()} title={t("settings.page.leaveTitle")} text={t("settings.subRules.leaveText")} confirm={t("settings.page.leaveConfirm")} danger onConfirm={() => leave.proceed?.()} />
    </div>
  );
}

/** What a rule says in one line: the first condition, how many more, and the answer. */
function summary(rule: Rule): string {
  const conds = [...rule.when.conds, ...rule.when.groups.flatMap((g) => g.conds)];
  let when = t("settings.subRules.everyRequest");
  if (conds[0]) {
    const c = conds[0];
    when = `${c.not ? `${t("settings.subRules.not")} ` : ""}${c.header} ${t(`settings.subRules.op.${c.op}`)} «${c.value}»${conds.length > 1 ? ` +${conds.length - 1}` : ""}`;
  }
  const format = rule.do.format ? ` · ${formatName(rule.do.format)}` : "";
  return `${when} → ${t(`settings.subRules.do.${rule.do.respond}`)}${format}`;
}

const formatName = (f: string) => tMaybe(`settings.subRules.format.${f}`) ?? f;

function RuleCard({
  i,
  n,
  rule,
  open,
  fires,
  errors,
  limits,
  formats,
  onToggle,
  onChange,
  onMove,
  onRemove,
}: {
  i: number;
  n: number;
  rule: Rule;
  open: boolean;
  fires: boolean;
  errors: Record<string, string>;
  limits: Limits;
  formats: string[];
  onToggle: () => void;
  onChange: (r: Rule) => void;
  onMove: (by: -1 | 1) => void;
  onRemove: () => void;
}) {
  const id = `sub-rule-${i}`;
  const at = `rules[${i}]`;
  const wrong = Object.keys(errors).some((k) => k === at || k.startsWith(`${at}.`));
  return (
    <li className="panel-soft" style={wrong ? { borderColor: "var(--berry-600)" } : undefined}>
      <div className="flex flex-wrap items-center gap-3 p-3">
        <Switch checked={rule.enabled} label={t("settings.subRules.enabled", { name: rule.name })} onChange={(enabled) => onChange({ ...rule, enabled })} />
        <button type="button" className={`min-w-0 flex-1 basis-40 text-left ${rule.enabled ? "" : "opacity-60"}`} aria-expanded={open} aria-controls={id} onClick={onToggle}>
          <span className="block truncate text-[14px] font-medium">{rule.name || t("settings.subRules.unnamed")}</span>
          <span className="line-clamp-2 text-xs break-words text-[var(--ink-500)]">{summary(rule)}</span>
        </button>
        {fires ? <Pill tone="ok">{t("settings.subRules.fires")}</Pill> : null}
        {wrong && !open ? <Pill tone="bad">{t("settings.subRules.hasError")}</Pill> : null}
        <div className="flex items-center">
          <Button size="sm" variant="ghost" aria-label={t("settings.subRules.up", { name: rule.name })} title={t("settings.subRules.up", { name: rule.name })} disabled={i === 0} onClick={() => onMove(-1)}>
            <ChevronUp size={16} aria-hidden />
          </Button>
          <Button size="sm" variant="ghost" aria-label={t("settings.subRules.down", { name: rule.name })} title={t("settings.subRules.down", { name: rule.name })} disabled={i === n - 1} onClick={() => onMove(1)}>
            <ChevronDown size={16} aria-hidden />
          </Button>
          <Button size="sm" variant="danger" aria-label={t("settings.subRules.remove", { name: rule.name })} title={t("settings.subRules.remove", { name: rule.name })} onClick={onRemove}>
            <Trash2 size={16} aria-hidden />
          </Button>
        </div>
      </div>
      {open ? (
        <div id={id} className="flex flex-col gap-4 border-t border-[var(--hairline)] p-4">
          <Field label={t("settings.subRules.name")} htmlFor={`${id}-name`} error={errors[`${at}.name`]}>
            <input id={`${id}-name`} className="input" value={rule.name} maxLength={limits.name} aria-invalid={!!errors[`${at}.name`]} onChange={(e) => onChange({ ...rule, name: e.target.value })} />
          </Field>
          <section aria-labelledby={`${id}-when`}>
            <h4 id={`${id}-when`} className="mb-2 text-[13px] font-semibold">
              {t("settings.subRules.when")}
            </h4>
            <WhenEditor when={rule.when} onChange={(when) => onChange({ ...rule, when })} errors={errors} at={`${at}.when`} limits={limits} />
          </section>
          <section aria-labelledby={`${id}-do`}>
            <h4 id={`${id}-do`} className="mb-2 text-[13px] font-semibold">
              {t("settings.subRules.then")}
            </h4>
            <ActionEditor id={id} at={`${at}.do`} a={rule.do} onChange={(a) => onChange({ ...rule, do: a })} errors={errors} limits={limits} formats={formats} />
          </section>
        </div>
      ) : null}
    </li>
  );
}

function ActionEditor({ id, at, a, onChange, errors, limits, formats }: { id: string; at: string; a: Action; onChange: (a: Action) => void; errors: Record<string, string>; limits: Limits; formats: string[] }) {
  const set = (patch: Partial<Action>) => onChange({ ...a, ...patch });
  const has = (...responds: Action["respond"][]) => responds.includes(a.respond);
  const headers = a.headers ?? [];
  const setHeader = (j: number, patch: Partial<{ name: string; value: string }>) => set({ headers: headers.map((h, k) => (k === j ? { ...h, ...patch } : h)) });
  return (
    <div className="flex flex-col gap-3">
      <Field label={t("settings.subRules.respond")} htmlFor={`${id}-respond`} hint={t(`settings.subRules.respondHint.${a.respond}`)} error={errors[`${at}.respond`]}>
        <Select id={`${id}-respond`} className="input max-w-[320px]" value={a.respond} onChange={(e) => set({ respond: e.target.value as Action["respond"] })}>
          {RESPONDS.map((r) => (
            <option key={r} value={r}>
              {t(`settings.subRules.do.${r}`)}
            </option>
          ))}
        </Select>
      </Field>
      {has("serve", "stub") ? (
        <Field label={t("settings.subRules.formatLabel")} htmlFor={`${id}-format`} hint={t("settings.subRules.formatHint")} error={errors[`${at}.format`]}>
          <Select id={`${id}-format`} className="input max-w-[320px]" value={a.format ?? ""} onChange={(e) => set({ format: e.target.value })}>
            <option value="">{t("settings.subRules.formatAuto")}</option>
            {formats.map((f) => (
              <option key={f} value={f}>
                {formatName(f)}
              </option>
            ))}
          </Select>
        </Field>
      ) : null}
      {has("serve") ? <TemplateSelect id={id} at={at} a={a} set={set} errors={errors} /> : null}
      {has("serve") ? <SwitchRow label={t("settings.subRules.skipHwid")} sub={t("settings.subRules.skipHwidSub")} checked={!!a.skip_hwid} onChange={(skip_hwid) => set({ skip_hwid })} /> : null}
      {has("stub") ? (
        <Field label={t("settings.subRules.stubText")} htmlFor={`${id}-text`} hint={t("settings.subRules.stubTextHint")} error={errors[`${at}.text`]}>
          <input id={`${id}-text`} className="input" value={a.text ?? ""} maxLength={limits.text} aria-invalid={!!errors[`${at}.text`]} onChange={(e) => set({ text: e.target.value })} />
        </Field>
      ) : null}
      {has("html") ? (
        <Field label={t("settings.subRules.page")} htmlFor={`${id}-html`} hint={t("settings.subRules.pageHint", { used: (a.html ?? "").length, max: limits.html })} error={errors[`${at}.html`]}>
          <textarea id={`${id}-html`} className="input mono min-h-[160px]" value={a.html ?? ""} maxLength={limits.html} spellCheck={false} aria-invalid={!!errors[`${at}.html`]} onChange={(e) => set({ html: e.target.value })} />
        </Field>
      ) : null}
      {has("drop") ? <p className="text-xs text-[var(--ink-500)]">{t("settings.subRules.dropNote")}</p> : null}
      {has("serve", "stub", "403", "404", "html") ? (
        <div>
          <div className="mb-2 text-[13px] font-medium text-[var(--ink-600)]">{t("settings.subRules.extraHeaders")}</div>
          {headers.length > 0 ? (
            <ul className="mb-2 flex flex-col gap-2">
              {headers.map((h, j) => {
                const error = errors[`${at}.headers[${j}].name`] ?? errors[`${at}.headers[${j}].value`];
                return (
                  <li key={j} className="flex flex-wrap items-center gap-2">
                    <input className="input mono w-[184px]" value={h.name} maxLength={64} spellCheck={false} autoCapitalize="off" placeholder="X-Header" aria-label={t("settings.subRules.header")} aria-invalid={!!errors[`${at}.headers[${j}].name`]} onChange={(e) => setHeader(j, { name: e.target.value })} />
                    <input className="input mono min-w-[160px] flex-1" value={h.value} maxLength={512} spellCheck={false} autoCapitalize="off" aria-label={t("settings.subRules.value")} aria-invalid={!!errors[`${at}.headers[${j}].value`]} onChange={(e) => setHeader(j, { value: e.target.value })} />
                    <Button size="sm" variant="ghost" aria-label={t("settings.subRules.removeHeader")} title={t("settings.subRules.removeHeader")} onClick={() => set({ headers: headers.filter((_, k) => k !== j) })}>
                      <X size={16} aria-hidden />
                    </Button>
                    {error ? (
                      <p className="basis-full text-xs text-[var(--berry-600)]" role="alert">
                        {error}
                      </p>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          ) : null}
          {errors[`${at}.headers`] ? (
            <p className="mb-2 text-xs text-[var(--berry-600)]" role="alert">
              {errors[`${at}.headers`]}
            </p>
          ) : null}
          <Button size="sm" disabled={headers.length >= limits.headers} onClick={() => set({ headers: [...headers, { name: "", value: "" }] })}>
            <Plus size={16} aria-hidden /> {t("settings.subRules.addHeader")}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

// The cores whose templates a profile of the format is made of: its own, and Clash's, which
// sing-box and Xray read when they have none of their own. The links have no template.
const FORMAT_CORES: Record<string, Schemas["SubTemplateHead"]["core"][]> = { clash: ["mihomo"], singbox: ["singbox", "mihomo"], xray: ["xray", "mihomo"], uri: [] };

/** The template the profile is made of, by the cores the rule's format reads; none: the core's default. */
function TemplateSelect({ id, at, a, set, errors }: { id: string; at: string; a: Action; set: (patch: Partial<Action>) => void; errors: Record<string, string> }) {
  const all = useSubTemplates().data?.templates ?? [];
  const cores = a.format ? (FORMAT_CORES[a.format] ?? CORES) : CORES;
  // A template a change of the format left out stays listed, so that the select shows what is saved.
  const listed = all.filter((x) => cores.includes(x.core) || x.id === a.template);
  if (cores.length === 0 && !a.template) return null;
  return (
    <Field label={t("settings.subRules.template")} htmlFor={`${id}-template`} hint={t("settings.subRules.templateHint")} error={errors[`${at}.template`]}>
      <Select id={`${id}-template`} className="input max-w-[320px]" value={String(a.template ?? 0)} onChange={(e) => set({ template: Number(e.target.value) || undefined })}>
        <option value="0">{t("settings.subRules.templateDefault")}</option>
        {CORES.filter((c) => listed.some((x) => x.core === c)).map((c) => (
          <optgroup key={c} label={coreName(c)}>
            {listed
              .filter((x) => x.core === c)
              .map((x) => (
                <option key={x.id} value={String(x.id)}>
                  {x.default ? `${x.name} · ${t("settings.subTemplates.default")}` : x.name}
                </option>
              ))}
          </optgroup>
        ))}
      </Select>
    </Field>
  );
}
