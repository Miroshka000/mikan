import { Link } from "@tanstack/react-router";
import { Card } from "../../../../components/layout";
import { t, useLocale } from "../../../../i18n";
import { CORES, coreName, kindName, servedOf, type View } from "./model";

/**
 * What the apps of each core get now when no response rule and no group of clients picks
 * another template: their core's default, the Clash default read their way, or the built-in
 * profile of the routing settings, which the card links to.
 */
export function AppsSummary({ v }: { v: View }) {
  useLocale();
  return (
    <Card title={t("settings.subTemplates.summary.title")} sub={t("settings.subTemplates.summary.lead")}>
      <ul className="grid gap-2 md:grid-cols-3">
        {CORES.map((core) => {
          const served = servedOf(v.templates, core);
          return (
            <li key={core} className="panel-soft flex min-w-0 flex-col gap-1 p-3">
              <span className="text-xs font-semibold text-[var(--ink-500)]">{t(`settings.subTemplates.summary.apps.${core}`)}</span>
              {served.by === "builtin" ? (
                <>
                  <span className="text-[14px] font-medium text-[var(--ink-900)]">{t("settings.subTemplates.summary.builtin")}</span>
                  <Link to="/settings" search={{ tab: "routing" }} className="link-btn w-fit text-xs">
                    {t("settings.subTemplates.summary.toRouting")}
                  </Link>
                </>
              ) : (
                <>
                  <span className="truncate text-[14px] font-medium text-[var(--ink-900)]" title={served.template.name}>
                    {served.template.name}
                  </span>
                  <span className="text-xs text-[var(--ink-500)]">
                    {served.by === "own"
                      ? t("settings.subTemplates.summary.own", { kind: kindName(served.template) })
                      : t(served.template.kind === "simple" ? "settings.subTemplates.summary.asClashSimple" : "settings.subTemplates.summary.asClash", { core: coreName("mihomo") })}
                  </span>
                </>
              )}
            </li>
          );
        })}
      </ul>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.subTemplates.summary.note")}</p>
    </Card>
  );
}
