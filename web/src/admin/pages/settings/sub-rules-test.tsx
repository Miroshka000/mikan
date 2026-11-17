import { useMutation } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../api/client";
import { useSubTemplates } from "../../../api/hooks";
import { Card } from "../../../components/layout";
import { Button, Field, Pill } from "../../../components/ui";
import { t } from "../../../i18n";
import { cleanRule, namedHeaders, ruleLabel } from "../../../lib/sub-rules";
import { RequestHeaders } from "./request-headers";

type Header = Schemas["Header"];
type Result = Schemas["SubRuleTest"];

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
          body: { user_agent: agent.trim(), headers: namedHeaders(headers), rules: rules.map(cleanRule), builtin },
        }),
      ),
    onSuccess: (r) => onResult(r.position),
    onError: () => onResult(0),
  });
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
        <RequestHeaders headers={headers} onChange={setHeaders} />
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
  const templates = useSubTemplates().data?.templates ?? [];
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
      {r.respond === "serve" && r.format && r.format !== "uri" ? (
        <>
          <dt className="text-[var(--ink-500)]">{t("settings.subRules.test.template")}</dt>
          <dd className="break-words">{r.template ? (templates.find((x) => x.id === r.template)?.name ?? `#${r.template}`) : t("settings.subRules.test.builtinProfile")}</dd>
        </>
      ) : null}
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
