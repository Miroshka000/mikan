import { useMutation } from "@tanstack/react-query";
import { useEffect } from "react";
import { api, errorText, unwrap, type Schemas } from "../../../../api/client";
import { Button } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { CoreNotes } from "../core-notes";
import type { Mode, Routes } from "./model";

// The built-in profile a user with every connection gets, before anything is saved: the
// unsaved rules too.
export function PreviewCard({ mode, routes, rules }: { mode: Mode; routes: Routes; rules: string }) {
  const preview = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/settings/routes/preview", { body: { sub_routing: mode, sub_routes: routes, sub_rules: rules } })),
  });
  // Shown at once when the panel opens; the button asks again after more changes.
  const { mutate } = preview;
  useEffect(() => mutate(), [mutate]);
  return (
    <section>
      <p className="mb-3 text-[13px] text-[var(--ink-500)]">{t("settings.routesPreviewSub")}</p>
      <Button variant="ghost" loading={preview.isPending} onClick={() => preview.mutate()}>
        {preview.data ? t("settings.routesPreviewAgain") : t("settings.routesPreviewShow")}
      </Button>
      {preview.error ? (
        <p className="mt-3 text-xs text-[var(--berry-600)]" role="alert">
          {errorText(preview.error)}
        </p>
      ) : null}
      {preview.data?.notes.length ? <PreviewNotes notes={preview.data.notes} /> : null}
      {preview.data ? <pre className="panel-soft mono mt-3 max-h-[70vh] overflow-auto p-3 text-[12px] leading-[1.45]">{preview.data.profile}</pre> : null}
    </section>
  );
}

/** What the sing-box and Xray apps make of the profile: the nearest kind of a group they have none of, and what they leave out. */
function PreviewNotes({ notes }: { notes: Schemas["Note"][] }) {
  return (
    <section className="mt-4" aria-labelledby="routes-notes">
      <h3 id="routes-notes" className="text-[13px] font-semibold text-[var(--ink-700)]">
        {t("settings.routesNotesTitle")}
      </h3>
      <p className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.routesNotesSub")}</p>
      <CoreNotes notes={notes} className="mt-2" />
    </section>
  );
}
