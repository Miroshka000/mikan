import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useMemo, type ReactNode } from "react";
import { api, errorText, unwrap } from "../../../../api/client";
import { CardHead } from "../../../../components/layout";
import { Pill, Skeleton } from "../../../../components/ui";
import { t, useLocale } from "../../../../i18n";
import { useDebounced } from "../../../../lib/debounce";
import { CoreNotes } from "../core-notes";
import type { Core, TemplateRouting } from "./model";

/** What a preview is asked of: the core's profile of a text, or of a simple template's routing. */
export type PreviewOf = { core: Core; body?: string; routing?: TemplateRouting };

/**
 * The profile a user with every connection gets of what is being edited: asked again once it
 * stands still, the last one kept in sight meanwhile. of is the caller's memo (a new one is an
 * edit); null is nothing to show yet, which empty says. aside goes into the head (the core a
 * simple template is seen in). bare: inside a drawer, without a card of its own.
 */
export function LivePreview({ of, empty, aside, bare }: { of: PreviewOf | null; empty?: string; aside?: ReactNode; bare?: boolean }) {
  useLocale();
  const settled = useDebounced(of, 600);
  // The query is keyed by what it holds, so that an edit undone asks nothing again.
  const key = useMemo(() => (settled ? JSON.stringify(settled) : ""), [settled]);
  const preview = useQuery({
    queryKey: ["sub-templates", "preview", key],
    queryFn: ({ signal }) => unwrap(api.POST("/api/v1/sub-templates/preview", { body: settled!, signal })),
    enabled: settled !== null,
    placeholderData: keepPreviousData,
    retry: false,
    // Each pause in the editing is a query keyed by the whole of it: the ones left behind go
    // soon, not after the default five minutes.
    staleTime: 30_000,
    gcTime: 30_000,
  });
  const waiting = settled !== of || preview.isFetching;
  const body = (
    <div aria-live="polite">
      {!of ? (
        <p className="text-[13px] text-[var(--ink-500)]">{empty}</p>
      ) : preview.isError && !waiting ? (
        <div className="banner err" role="alert">
          {errorText(preview.error)}
        </div>
      ) : preview.data ? (
        <>
          {preview.data.notes.length > 0 ? <CoreNotes notes={preview.data.notes} className="mb-3" /> : null}
          <pre className="panel-soft mono max-h-[70vh] overflow-auto p-3 text-[12px] leading-[1.45]" style={{ opacity: waiting ? 0.6 : 1 }}>
            {preview.data.profile}
          </pre>
        </>
      ) : (
        <Skeleton style={{ height: 320, borderRadius: 12 }} />
      )}
    </div>
  );
  if (bare) {
    return (
      <div className="flex flex-col gap-3">
        {aside ? <div className="flex flex-wrap items-center gap-2">{aside}</div> : null}
        {body}
      </div>
    );
  }
  return (
    <section className="card glass min-w-0 xl:sticky xl:top-4 xl:self-start" aria-labelledby="sub-template-preview">
      <CardHead
        id="sub-template-preview"
        title={t("settings.subTemplates.preview")}
        sub={t("settings.subTemplates.previewSub")}
        aside={
          <div className="flex flex-wrap items-center gap-2">
            {of && waiting ? <Pill tone="off">{t("settings.subTemplates.previewUpdating")}</Pill> : null}
            {aside}
          </div>
        }
      />
      {body}
    </section>
  );
}
