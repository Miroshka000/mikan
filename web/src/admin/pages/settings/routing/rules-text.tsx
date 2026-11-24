import { useRef } from "react";
import { t } from "../../../../i18n";

// Ready-made rules the admin adds with one click; PROXY is the main group's alias that
// survives renaming it.
const RULE_EXAMPLES = [
  { key: "siteDirect", rule: "DOMAIN-SUFFIX,example.com,DIRECT" },
  { key: "siteVpn", rule: "DOMAIN-SUFFIX,example.com,PROXY" },
  { key: "ads", rule: "GEOSITE,category-ads-all,REJECT" },
  { key: "app", rule: "PROCESS-NAME,Telegram.exe,PROXY" },
  { key: "lan", rule: "IP-CIDR,192.168.0.0/16,DIRECT,no-resolve" },
] as const;

/** The admin's own rules as text, a line each (routing.ParseRules checks them): the routing form's rules seen as text. */
export function RulesText({ value, onChange, error, badLine }: { value: string; onChange: (f: (v: string) => string) => void; error?: string; badLine: number }) {
  const gutter = useRef<HTMLDivElement>(null);
  const area = useRef<HTMLTextAreaElement>(null);
  const lines = value.split("\n");
  const add = (rule: string) => {
    onChange((v) => (v.trim() ? v.replace(/\s*$/, "\n") : "") + rule);
    requestAnimationFrame(() => {
      const el = area.current;
      if (!el) return;
      el.focus();
      el.selectionStart = el.selectionEnd = el.value.length;
      el.scrollTop = el.scrollHeight;
    });
  };
  return (
    <>
      <div className="rules-editor" aria-invalid={!!error}>
        <div className="rules-gutter" ref={gutter} aria-hidden>
          {lines.map((_, i) => (
            <div key={i} className={i + 1 === badLine ? "bad" : undefined}>
              {i + 1}
            </div>
          ))}
        </div>
        <textarea
          ref={area}
          id="s-rules"
          value={value}
          onChange={(e) => onChange(() => e.target.value)}
          onScroll={(e) => {
            if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop;
          }}
          spellCheck={false}
          autoCapitalize="off"
          autoComplete="off"
          wrap="off"
          maxLength={65536}
          placeholder={t("settings.rulesPlaceholder")}
          aria-label={t("settings.rules")}
          aria-invalid={!!error}
          aria-describedby="s-rules-hint"
        />
      </div>
      <RuleError error={error} />
      <RuleExamples onAdd={add} />
    </>
  );
}

export function RuleError({ error }: { error?: string }) {
  return error ? (
    <p className="mt-2 text-xs text-[var(--berry-600)]" role="alert">
      {error}
    </p>
  ) : null;
}

/** The ready-made rules, a chip each: onAdd gets the rule's line. */
export function RuleExamples({ onAdd }: { onAdd: (rule: string) => void }) {
  return (
    <div className="mt-3 flex flex-wrap gap-2" role="group" aria-label={t("settings.rulesExamples")}>
      {RULE_EXAMPLES.map((x) => (
        <button key={x.key} type="button" className="chip-btn" onClick={() => onAdd(x.rule)} title={x.rule}>
          + {t(`settings.rulesEx.${x.key}`)}
        </button>
      ))}
    </div>
  );
}

/** What a rule is written of and where it may send traffic: under either view of the rules. */
export function RulesHint({ targets }: { targets: string[] }) {
  return (
    <div id="s-rules-hint" className="mt-3 text-xs text-[var(--ink-500)]">
      <p>{t("settings.rulesHint")}</p>
      <p className="mt-1">
        {t("settings.rulesTargets")}{" "}
        {targets.map((x, i) => (
          <span key={x}>
            {i ? " · " : ""}
            <code className="mono">{x}</code>
          </span>
        ))}
      </p>
      <details className="mt-2">
        <summary className="cursor-pointer font-medium text-[var(--ink-700)]">{t("settings.rulesTypes")}</summary>
        <p className="mt-2">{t("settings.rulesTypesText")}</p>
      </details>
    </div>
  );
}
