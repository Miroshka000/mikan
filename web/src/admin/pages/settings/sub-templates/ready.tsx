import { ExternalLink } from "lucide-react";
import { useReadySubTemplates } from "../../../../api/hooks";
import { Drawer } from "../../../../components/overlay";
import { QueryBoundary } from "../../../../components/query";
import { Button, Pill, Skeleton } from "../../../../components/ui";
import { t, useLocale } from "../../../../i18n";
import { coreName, readyName, readySummary, type Core } from "./model";

/** The ready templates of a core, each with its author, license and source: picking one puts
 * its text in the editor and saves nothing. mikan's own is made of the panel's settings. */
export function ReadyTemplates({
  core,
  open,
  onOpenChange,
  loading,
  onPick,
}: {
  core: Core;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  loading?: string;
  onPick: (id: string) => void;
}) {
  useLocale();
  const q = useReadySubTemplates();
  return (
    <Drawer open={open} onOpenChange={onOpenChange} title={t("settings.subTemplates.ready.title", { core: coreName(core) })}>
      <p className="text-sm text-[var(--ink-500)]">{t("settings.subTemplates.ready.lead")}</p>
      <QueryBoundary query={q} pending={<Skeleton style={{ height: 160, borderRadius: 12 }} />}>
        {(v) => (
          <ul className="mt-4 flex flex-col gap-2">
            {v.templates
              .filter((r) => r.core === core)
              .map((r) => (
                <li key={r.id} className="panel-soft flex flex-col gap-2 p-3">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-[14px] font-medium">{readyName(r)}</span>
                    {r.builtin ? <Pill tone="ok">{t("settings.subTemplates.ready.builtin")}</Pill> : null}
                  </div>
                  <p className="text-xs text-[var(--ink-500)]">{readySummary(r)}</p>
                  <p className="text-xs break-words text-[var(--ink-500)]">
                    {t("settings.subTemplates.ready.meta", { author: r.author, license: r.license })}{" "}
                    <a className="link-btn inline-flex items-center gap-1" href={r.source} target="_blank" rel="noreferrer noopener">
                      {t("settings.subTemplates.ready.source")} <ExternalLink size={12} aria-hidden />
                    </a>
                  </p>
                  <div>
                    <Button size="sm" variant="ghost" loading={loading === r.id} disabled={loading !== undefined && loading !== r.id} onClick={() => onPick(r.id)}>
                      {t("settings.subTemplates.ready.load")}
                    </Button>
                  </div>
                </li>
              ))}
          </ul>
        )}
      </QueryBoundary>
    </Drawer>
  );
}
