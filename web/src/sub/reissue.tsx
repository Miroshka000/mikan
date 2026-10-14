import { Hourglass, RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "../components/ui";
import { t } from "../i18n";
import { dateShort, time } from "../lib/format";
import { initData, postJSON, subRoot, tokenOf } from "./net";
import type { Info } from "./types";

/** The reissue window in words: "сутки", "неделю", "10 дней". */
function period(days: number): string {
  if (days === 1) return t("sub.periodDay");
  if (days === 7) return t("sub.periodWeek");
  if (days === 30) return t("sub.periodMonth");
  return t("sub.periodDays", { n: days });
}

/**
 * The subscriber's own new link (the Mini App only): the old one stops working at once and
 * the apps need the new one, so a tap asks first. Telegram's launch data proves who asks;
 * on success the page opens the subscription at its new address.
 */
export function ReissueSection({ info, subURL, title, onDone }: { info: Info; subURL: string; title?: string; onDone: (token: string) => void }) {
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // The moment the limit lifts, as the server worked it out; a past one means "now".
  const [wait, setWait] = useState<string | undefined>(info.reissue_after);
  useEffect(() => {
    setWait(info.reissue_after);
  }, [info.reissue_after]);
  const waiting = wait && new Date(wait).getTime() > Date.now() ? wait : "";
  const limit = info.reissue_limit ?? 0;
  const off = !!info.reissue_off;

  if (!info.reissue) return null;

  const reissue = async () => {
    setBusy(true);
    setError("");
    try {
      const r = await postJSON<{ token: string; code: string; reissue_after: string }>(subRoot + "/tg/reissue", { init_data: initData, token: tokenOf(subURL) });
      if (r.ok && r.data.token) {
        setConfirm(false);
        onDone(r.data.token);
        return;
      }
      if (r.status === 429 && r.data.reissue_after) setWait(r.data.reissue_after);
      setError(r.status === 429 ? (r.data.reissue_after ? "" : t("sub.reissueFailed")) : r.data.code === "reissue_off" ? t("sub.reissueOff") : t("sub.reissueFailed"));
      setConfirm(false);
    } catch {
      setError(t("sub.reissueFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="glass sub-card p-4" aria-labelledby="reissue-title">
      <div className="mb-2 flex items-center gap-2">
        <RefreshCw size={16} className="text-[var(--ink-600)]" aria-hidden />
        <h2 id="reissue-title" className="text-[15px] font-semibold">
          {title || t("sub.reissueTitle")}
        </h2>
      </div>
      <p className="text-[13px] text-[var(--ink-600)]">{off ? t("sub.reissueOff") : t("sub.reissueText")}</p>
      {waiting ? (
        <div className="mt-3 flex items-start gap-2 rounded-2xl bg-[var(--hover)] p-3" role="status">
          <Hourglass size={18} className="mt-0.5 shrink-0 text-[var(--mikan-600)]" aria-hidden />
          <p className="text-[13px] text-[var(--ink-600)]">{t("sub.reissueAfter", { date: dateShort(waiting), time: time(waiting) })}</p>
        </div>
      ) : null}
      {off || waiting ? null : confirm ? (
        <div className="mt-3 rounded-2xl bg-[var(--hover)] p-3" role="group" aria-label={title || t("sub.reissueTitle")}>
          <p className="text-[13px] text-[var(--ink-700)]">{t("sub.reissueWarn")}</p>
          <div className="mt-2 flex justify-end gap-2">
            <Button
              variant="ghost"
              size="sm"
              disabled={busy}
              autoFocus
              onClick={() => {
                setConfirm(false);
                window.requestAnimationFrame(() => document.getElementById("reissue-open")?.focus());
              }}
            >
              {t("common.cancel")}
            </Button>
            <Button variant="danger-solid" size="sm" loading={busy} onClick={() => void reissue()}>
              {t("sub.reissueYes")}
            </Button>
          </div>
        </div>
      ) : (
        <div className="mt-3">
          <Button id="reissue-open" size="sm" onClick={() => setConfirm(true)}>
            {t("sub.reissueButton")}
          </Button>
        </div>
      )}
      {error ? (
        <p className="mt-2 text-[13px] text-[var(--berry-600)]" role="alert">
          {error}
        </p>
      ) : null}
      {!off && limit > 0 ? <p className="mt-2 text-xs text-[var(--ink-500)]">{t("sub.reissueLimit", { n: limit, period: period(info.reissue_days ?? 1) })}</p> : null}
    </section>
  );
}
