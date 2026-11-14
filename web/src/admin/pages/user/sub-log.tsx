import { errorText, type User } from "../../../api/client";
import { useUserSubLog } from "../../../api/hooks";
import { Pill, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { ago, appName, dateTime } from "../../../lib/format";
import { ruleLabel } from "../../../lib/sub-rules";
import { Section } from "./section";

const TONE = { served: "ok", stub: "warn", blocked: "bad", dropped: "bad" } as const;

/**
 * The latest requests of this subscription's link: when, from where, by which app, what
 * decided and what the app was given. The link itself and the keys are never in it. It is
 * kept for the retention the admin sets (Settings → Subscription), and not at all when that is 0.
 */
export function SubLogSection({ u }: { u: User }) {
  const log = useUserSubLog(u.id);
  const v = log.data;
  return (
    <Section title={t("userDrawer.subLog.title")} aside={v ? (v.days === 0 ? t("userDrawer.subLog.off") : t("userDrawer.subLog.kept", { n: v.days })) : undefined}>
      {log.isPending ? (
        <div className="flex flex-col gap-2">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} style={{ height: 44, borderRadius: 10 }} />
          ))}
        </div>
      ) : log.isError ? (
        <div className="banner err" role="alert">
          {errorText(log.error)}
        </div>
      ) : !v || v.items.length === 0 ? (
        <p className="text-xs text-[var(--ink-500)]">{v?.days === 0 ? t("userDrawer.subLog.offText") : t("userDrawer.subLog.none")}</p>
      ) : (
        <ul className="row-list">
          {v.items.map((it, i) => (
            <li key={`${it.at}-${i}`} className="flex flex-col gap-1 py-2">
              <div className="flex items-center justify-between gap-2">
                <span className="flex min-w-0 items-center gap-2">
                  <Pill tone={TONE[it.outcome]}>{t(`userDrawer.subLog.outcome.${it.outcome}`)}</Pill>
                  <span className="truncate text-[13px] font-medium" title={it.agent}>
                    {it.app_name || appName(it.agent) || t("userDrawer.subLog.noAgent")}
                  </span>
                </span>
                <time dateTime={it.at} title={dateTime(it.at)} className="num shrink-0 text-xs text-[var(--ink-500)]">
                  {ago(it.at)}
                </time>
              </div>
              <div className="mono truncate text-xs text-[var(--ink-500)]" title={it.agent}>
                {[it.ip, it.rule ? ruleLabel(it.rule) : "", it.format].filter(Boolean).join(" · ")}
              </div>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("userDrawer.subLog.hint")}</p>
    </Section>
  );
}
