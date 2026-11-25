import { CheckCircle2, Copy, Download } from "lucide-react";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";
import { useCopy } from "../../../lib/copy";
import { saveText } from "../../../lib/download";
import { csvOf, linksText, type BatchUser } from "./model";

/** The day a file of the batch is named by, as the admin's own calendar has it. */
const today = () => new Date().toLocaleDateString("sv");

/**
 * What a batch made: every client with its link, to copy one by one, all at once (a line each,
 * ready to send) or as a table. A caller who may not see links (a support operator) gets the
 * names alone.
 */
export function BatchResult({ users }: { users: BatchUser[] }) {
  const copy = useCopy();
  const links = users.every((u) => u.sub_url !== "");
  return (
    <div className="flex flex-col gap-4 pt-5">
      <div className="panel-soft flex items-start gap-3 p-4" role="status">
        <CheckCircle2 size={20} className="mt-0.5 shrink-0 text-[var(--leaf-700)]" aria-hidden />
        <div className="min-w-0">
          <div className="text-[14px] font-semibold text-[var(--ink-900)]">{t("userCreate.batchDone", { n: users.length })}</div>
          <div className="mt-1 text-xs text-[var(--ink-500)]">{links ? t("userCreate.batchDoneSub") : t("userCreate.batchNoLinks")}</div>
        </div>
      </div>
      {links ? (
        <div className="flex flex-wrap gap-2">
          {/* The form the focus was in is gone: it goes to what is done next, not to the drawer. */}
          <Button size="sm" autoFocus onClick={() => void copy(linksText(users), t("userCreate.batchCopied", { n: users.length }))}>
            <Copy size={16} aria-hidden /> {t("userCreate.batchCopyAll")}
          </Button>
          <Button size="sm" onClick={() => saveText(`mikan-clients-${today()}.csv`, csvOf(users), "text/csv")}>
            <Download size={16} aria-hidden /> {t("userCreate.batchCsv")}
          </Button>
        </div>
      ) : null}
      <ul className="row-list max-h-[50vh] overflow-y-auto" tabIndex={0} aria-label={t("userCreate.batchList")}>
        {users.map((u) => (
          <li key={u.id} className="flex items-center justify-between gap-3 py-2">
            <div className="min-w-0">
              <div className="truncate text-[13px] font-medium text-[var(--ink-900)]">{u.name}</div>
              {u.sub_url ? <div className="mono truncate text-xs text-[var(--ink-500)]">{u.sub_url}</div> : null}
            </div>
            {u.sub_url ? (
              <Button
                size="sm"
                variant="ghost"
                aria-label={t("userCreate.batchCopyOne", { name: u.name })}
                title={t("userCreate.batchCopyOne", { name: u.name })}
                onClick={() => void copy(u.sub_url, t("userCreate.batchCopiedOne", { name: u.name }))}
              >
                <Copy size={16} aria-hidden />
              </Button>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}
