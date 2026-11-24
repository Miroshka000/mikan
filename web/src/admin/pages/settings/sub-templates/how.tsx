import { t } from "../../../../i18n";
import type { Core } from "./model";

/** How the user's servers get into the groups of a template of the core, and the nodes to name. */
export function HowItWorks({ core, names }: { core: Core; names: string[] }) {
  const node = names[0] ?? "1";
  const country = t("settings.subTemplates.sample.country");
  const auto = t("settings.subTemplates.sample.auto");
  const example =
    core === "mihomo"
      ? `- name: ${country}
  type: url-test
  include-all-proxies: true
  filter: "🇩🇪"

- name: ${t("settings.subTemplates.sample.fast")}
  type: url-test
  mikan:
    nodes: [${names.slice(0, 2).map((n) => `"${n}"`).join(", ") || '"1"'}]
    types: [vless, hysteria2]`
      : core === "singbox"
        ? `{"type": "urltest", "tag": "${auto}", "mikan": {}},
{"type": "selector", "tag": "🔒 VLESS",
 "mikan": {"nodes": ["${node}"], "types": ["vless"]}}`
        : `"balancers": [
  {"tag": "${auto}", "mikan": {"choice": true},
   "strategy": {"type": "leastPing"}},
  {"tag": "${country}", "mikan": {"filter": "🇩🇪"},
   "strategy": {"type": "random"}}
]`;
  return (
    <details className="text-xs text-[var(--ink-500)]">
      <summary className="cursor-pointer font-medium text-[var(--ink-700)]">{t("settings.subTemplates.how")}</summary>
      <p className="mt-2">{t(`settings.subTemplates.howServers.${core}`)}</p>
      <pre className="panel-soft mono mt-2 overflow-auto p-2 text-[12px]">{example}</pre>
      <p className="mt-2">{t("settings.subTemplates.howNodes")}</p>
      <div className="mt-1 flex flex-wrap gap-1.5">
        {names.map((n) => (
          <code key={n} className="tag mono">
            {n}
          </code>
        ))}
      </div>
      <p className="mt-2">{t(`settings.subTemplates.howRest.${core}`)}</p>
    </details>
  );
}
