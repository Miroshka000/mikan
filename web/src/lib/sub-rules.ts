import type { Schemas } from "../api/client";
import { t } from "../i18n";

/**
 * What decided a request, in words. The API names an admin's rule by its own name and the
 * panel's decisions by a key: "builtin:ssr" and "builtin:old" are the built-in rules,
 * "apps" the list of blocked apps; "" is nothing, the request was served as usual.
 */
export function ruleLabel(rule: string): string {
  switch (rule) {
    case "":
      return t("settings.subRules.rule.none");
    case "apps":
      return t("settings.subRules.rule.apps");
    case "builtin:ssr":
      return t("settings.subRules.rule.ssr");
    case "builtin:old":
      return t("settings.subRules.rule.old");
    default:
      return rule;
  }
}

type Rule = Schemas["Rule"];
type Action = Schemas["Action"];

const takes = (a: Action, ...responds: Action["respond"][]) => responds.includes(a.respond);

/**
 * The rule as the API takes it: what its answer does not use is left out (the panel does the
 * same), so the leftovers of an answer the admin changed their mind about are neither saved
 * nor refused.
 */
export function cleanRule(r: Rule): Rule {
  const a = r.do;
  const out: Action = { respond: a.respond };
  if (takes(a, "serve", "stub") && a.format) out.format = a.format;
  if (takes(a, "serve") && a.skip_hwid) out.skip_hwid = true;
  if (takes(a, "stub") && a.text?.trim()) out.text = a.text.trim();
  if (takes(a, "html")) out.html = a.html ?? "";
  if (takes(a, "serve", "stub", "403", "404", "html") && a.headers?.length) out.headers = a.headers;
  return { ...r, name: r.name.trim(), do: out };
}
