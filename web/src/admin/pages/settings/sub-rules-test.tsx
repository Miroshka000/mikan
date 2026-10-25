import { useMutation } from "@tanstack/react-query";
import { Plus, X } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { Card } from "../../../components/layout";
import { Button, Field, Pill } from "../../../components/ui";
import { t } from "../../../i18n";
import { cleanRule, ruleLabel } from "../../../lib/sub-rules";
import { HEADER_LIST } from "./sub-rules-when";

type Header = Schemas["Header"];
type Result = Schemas["SubRuleTest"];

// Headers a request most often differs by, one tap to add.
const QUICK = ["X-Device-Os", "X-Hwid", "Accept"] as const;

/**
 * Which rule fires on a request: paste a User-Agent and the headers that matter, and see the
 * rule, the app and the answer, by the rules as they are on screen, saved or not.
 */
export function RuleTester({ rules, builtin, onResult }: { rules: Schemas["Rule"][]; builtin: boolean; onResult: (position: number) => void }) {
  const [agent, setAgent] = useState("");
  const [headers, setHeaders] = useState<Header[]>([]);
  const run = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/api/v1/sub-rules/test", {
          body: { user_agent: agent.trim(), headers: headers.filter((h) => h.name.trim() !== "").map((h) => ({ name: h.name.trim(), value: h.value })), rules: rules.map(cleanRule), builtin },
        }),
      ),
    onSuccess: (r) => onResult(r.position),
    onError: () => onResult(0),
  });
  const setHeader = (i: number, patch: Partial<Header>) => setHeaders((hs) => hs.map((h, j) => (j === i ? { ...h, ...patch } : h)));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    run.mutate();
  };
  return (
    <Card title={t("settings.subRules.test.title")} sub={t("settings.subRules.test.sub")} i={3}>
      <form onSubmit={submit} noValidate>
        <Field label={t("settings.subRules.test.agent")} htmlFor="rule-test-agent" hint={t("settings.subRules.test.agentHint")}>
          <input id="rule-test-agent" className="input mono" value={agent} maxLength={1024} spellCheck={false} autoCapitalize="off" autoComplete="off" placeholder="Happ/3.6.0 (iOS 18.1)" onChange={(e) => setAgent(e.target.value)} />
        </Field>
        {headers.length > 0 ? (
          <ul className="mb-3 flex flex-col gap-2">
            {headers.map((h, i) => (
              <li key={i} className="flex flex-wrap items-center gap-2">
                <input className="input mono w-[168px]" list={HEADER_LIST} value={h.name} maxLength={64} spellCheck={false} autoCapitalize="off" aria-label={t("settings.subRules.header")} onChange={(e) => setHeader(i, { name: e.target.value })} />
                <input className="input mono min-w-[160px] flex-1" value={h.value} maxLength={512} spellCheck={false} autoCapitalize="off" aria-label={t("settings.subRules.value")} onChange={(e) => setHeader(i, { value: e.target.value })} />
                <Button size="sm" variant="ghost" aria-label={t("settings.subRules.test.removeHeader")} title={t("settings.subRules.test.removeHeader")} onClick={() => setHeaders((hs) => hs.filter((_, j) => j !== i))}>
                  <X size={16} aria-hidden />
                </Button>
              </li>
            ))}
          </ul>
        ) : null}
        <div className="mb-4 flex flex-wrap items-center gap-2" role="group" aria-label={t("settings.subRules.test.headers")}>
          <span className="text-xs text-[var(--ink-500)]">{t("settings.subRules.test.headers")}</span>
          {QUICK.map((name) => (
            <button key={name} type="button" className="chip-btn" disabled={headers.length >= 30} onClick={() => setHeaders((hs) => [...hs, { name, value: "" }])}>
              <Plus size={12} className="mr-1 inline" aria-hidden />
              <span className="mono">{name}</span>
            </button>
          ))}
        </div>
        <Button type="submit" variant="primary" loading={run.isPending}>
          {t("settings.subRules.test.run")}
        </Button>
      </form>
      <div aria-live="polite">
        {run.isError ? (
          <div className="banner err mt-4" role="alert">
            {errorText(run.error)}
          </div>
        ) : null}
        {run.data ? <Answer r={run.data} /> : null}
      </div>
    </Card>
  );
}

function Answer({ r }: { r: Result }) {
  const respond =
    r.respond === "serve"
      ? t("settings.subRules.test.serve")
      : r.respond === "stub"
        ? t("settings.subRules.test.stub")
        : r.respond === "html"
          ? t("settings.subRules.test.html")
          : r.respond === "drop"
            ? t("settings.subRules.test.drop")
            : t("settings.subRules.test.status", { code: r.status });
  const decided = r.position > 0 ? `${ruleLabel(r.rule)} · ${t("settings.subRules.test.position", { n: r.position })}` : ruleLabel(r.rule);
  return (
    <dl className="panel-soft mt-4 grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-4 gap-y-2 p-4 text-[13px]">
      <dt className="text-[var(--ink-500)]">{t("settings.subRules.test.app")}</dt>
      <dd>{r.app_name || t("settings.subRules.test.unknownApp")}</dd>
      <dt className="text-[var(--ink-500)]">{t("settings.subRules.test.decided")}</dt>
      <dd>{decided}</dd>
      <dt className="text-[var(--ink-500)]">{t("settings.subRules.test.answer")}</dt>
      <dd className="flex flex-wrap items-center gap-2">
        <Pill tone={r.respond === "serve" ? "ok" : r.respond === "stub" || r.respond === "html" ? "warn" : "bad"}>{respond}</Pill>
        {r.format ? <span className="mono">{r.format}</span> : null}
      </dd>
      {r.text ? (
        <>
          <dt className="text-[var(--ink-500)]">{t("settings.subRules.test.text")}</dt>
          <dd className="break-words">{r.text}</dd>
        </>
      ) : null}
      {r.skip_hwid ? (
        <>
          <dt className="text-[var(--ink-500)]">{t("settings.subRules.test.hwid")}</dt>
          <dd>{t("settings.subRules.test.hwidSkipped")}</dd>
        </>
      ) : null}
      {r.headers.length > 0 ? (
        <>
          <dt className="text-[var(--ink-500)]">{t("settings.subRules.test.extra")}</dt>
          <dd className="mono break-all">{r.headers.map((h) => `${h.name}: ${h.value}`).join("\n")}</dd>
        </>
      ) : null}
    </dl>
  );
}
