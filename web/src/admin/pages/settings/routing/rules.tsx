import { useState } from "react";
import { Card } from "../../../../components/layout";
import { t } from "../../../../i18n";
import { RuleRows } from "./rule-rows";
import { RulesHint, RulesText } from "./rules-text";

// The admin's choice of the view, kept in this browser: it is theirs, not the panel's.
const AS_TEXT = "mikan.rulesAsText";

function storedAsText(): boolean {
  try {
    return localStorage.getItem(AS_TEXT) === "1";
  } catch {
    return false; // storage closed (a private window): the rows
  }
}

/**
 * The admin's own rules, as rows put in order by dragging or, with "As text", as the text they
 * are saved as. Both edit the same text, line for line. error and badLine are a refusal's.
 */
export function RulesPart({ value, onChange, error, badLine, targets, sub, i }: { value: string; onChange: (f: (v: string) => string) => void; error?: string; badLine: number; targets: string[]; sub: string; i?: number }) {
  const [asText, setAsText] = useState(storedAsText);
  const toggle = (on: boolean) => {
    setAsText(on);
    try {
      localStorage.setItem(AS_TEXT, on ? "1" : "0");
    } catch {
      // Nothing to keep it in: the view holds for this page.
    }
  };
  return (
    <Card
      title={t("settings.rules")}
      sub={sub}
      i={i}
      aside={
        <button type="button" className="chip-btn" aria-pressed={asText} onClick={() => toggle(!asText)}>
          {t("settings.rulesRows.asText")}
        </button>
      }
    >
      {asText ? <RulesText value={value} onChange={onChange} error={error} badLine={badLine} /> : <RuleRows value={value} onChange={onChange} error={error} badLine={badLine} targets={targets} />}
      <RulesHint targets={targets} />
    </Card>
  );
}
