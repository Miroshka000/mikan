import clsx from "clsx";
import type { Schemas } from "../../../api/client";
import { t } from "../../../i18n";

/**
 * What the sing-box and Xray apps make of a profile their own way: the nearest kind of a group
 * they have none of, and what they leave out. The routing preview, a template's preview and
 * the preview of a client's subscription show them.
 */
export function CoreNotes({ notes, className }: { notes: Schemas["Note"][]; className?: string }) {
  return (
    <ul className={clsx("flex flex-col gap-2", className)}>
      {notes.map((n) => (
        <li key={`${n.core}/${n.group}/${n.code}`} className="panel-soft flex flex-wrap gap-x-2 gap-y-1 p-3 text-[13px]">
          <span className="font-medium">{t(`settings.subTemplates.cores.${n.core}`)}</span>
          <span className="min-w-0 break-words text-[var(--ink-700)]">{t(`settings.routesNotes.${n.code}`, { group: n.group })}</span>
        </li>
      ))}
    </ul>
  );
}
