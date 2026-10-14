import { ExternalLink } from "lucide-react";
import type { Schemas } from "../../../api/client";
import { useUpdates } from "../../../api/hooks";
import { Pill } from "../../../components/ui";
import { getLocale, t } from "../../../i18n";

type Goal = Schemas["Goal"];

/** The docs site's page with every goal and what is done. */
const goalsPage = (lang: string) => `https://o0hhs.github.io/mikanaddons/${lang === "en" ? "en/" : ""}support/`;

/**
 * What the project collects money for, as the signed release index says (it comes with
 * the update check, nothing else is asked). Hidden by the switch on the updates card, and
 * absent until a check found goals.
 */
export function GoalsCard() {
  const u = useUpdates();
  const v = u.data;
  const active = (v?.goals ?? []).filter((g) => g.status !== "done").slice(0, 3);
  if (!v || !v.show_goals || active.length === 0) return null;
  const lang = getLocale();
  const text = (m?: Record<string, string>) => m?.[lang] || m?.en || m?.ru || "";
  const money = (n: number, currency: string) => new Intl.NumberFormat(lang, { style: "currency", currency, maximumFractionDigits: 0 }).format(n);
  return (
    <section className="card glass reveal" aria-labelledby="goals-title">
      <div className="card-head">
        <div>
          <h2 className="card-title" id="goals-title">
            {t("goals.title")}
          </h2>
          <div className="card-sub">{t("goals.sub")}</div>
        </div>
      </div>
      <ul className="row-list">
        {active.map((g: Goal) => {
          const pct = Math.min(100, Math.round((g.raised / g.target) * 100));
          const raised = money(g.raised, g.currency);
          const target = money(g.target, g.currency);
          return (
            <li key={g.id} className="py-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="text-[13px] font-semibold text-[var(--ink-900)]">{text(g.title)}</div>
                  {text(g.about) ? <p className="mt-1 text-xs text-[var(--ink-500)]">{text(g.about)}</p> : null}
                </div>
                <Pill tone={g.status === "working" ? "ok" : "warn"}>{g.status === "working" ? t("goals.working") : t("goals.open")}</Pill>
              </div>
              <div className="bar mt-3" role="progressbar" aria-valuemin={0} aria-valuemax={g.target} aria-valuenow={Math.min(g.raised, g.target)} aria-label={t("goals.progress", { title: text(g.title), raised, target })}>
                <i style={{ width: `${pct}%` }} />
              </div>
              <div className="mt-2 flex flex-wrap items-center justify-between gap-2 text-xs text-[var(--ink-500)]">
                <span>
                  <b className="num font-semibold text-[var(--ink-900)]">{t("goals.raised", { raised, target })}</b> · {pct}%
                </span>
                {g.status === "open" ? (
                  <a className="btn btn-primary btn-sm" href={g.url || v.donate} target="_blank" rel="noopener noreferrer">
                    {t("goals.give")}
                  </a>
                ) : null}
              </div>
            </li>
          );
        })}
      </ul>
      <div className="form-actions">
        <a className="link-btn form-actions-note inline-flex items-center gap-1 text-[13px]" href={goalsPage(lang)} target="_blank" rel="noopener noreferrer">
          {t("goals.all")} <ExternalLink size={14} aria-hidden />
        </a>
        {v.donate ? (
          <a className="btn btn-glass btn-sm" href={v.donate} target="_blank" rel="noopener noreferrer">
            {t("goals.any")}
          </a>
        ) : null}
      </div>
    </section>
  );
}
