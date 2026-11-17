import { useMutation } from "@tanstack/react-query";
import { api, errorText, unwrap, type User } from "../../../../api/client";
import { useUserSubLog } from "../../../../api/hooks";
import { Skeleton } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { Section } from "../section";
import { PreviewForm } from "./form";
import { recentAgents, type PreviewRequest } from "./model";
import { PreviewResult } from "./result";

// The apps of the client's latest requests offered one tap away.
const RECENT = 4;

/**
 * What the client gets (#290): the answer to a request of this subscription from the app of
 * the headers the admin writes, by the very rules, templates, group and addresses a real
 * fetch meets. Nothing changes: the keys are placeholders, no device is bound, nothing is logged.
 */
export function PreviewSection({ u }: { u: User }) {
  const log = useUserSubLog(u.id);
  const run = useMutation({
    mutationFn: (body: PreviewRequest) => unwrap(api.POST("/api/v1/users/{id}/sub-preview", { params: { path: { id: u.id } }, body })),
  });
  return (
    <Section title={t("userDrawer.preview.title")}>
      <p className="mb-4 text-xs text-[var(--ink-500)]">{t("userDrawer.preview.sub")}</p>
      <PreviewForm agents={recentAgents(log.data?.items ?? [], RECENT)} busy={run.isPending} onSubmit={(body) => run.mutate(body)} />
      <div aria-live="polite" aria-busy={run.isPending}>
        {run.isPending ? (
          <div className="mt-4 flex flex-col gap-2">
            <Skeleton style={{ height: 152, borderRadius: 12 }} />
            <Skeleton style={{ height: 96, borderRadius: 12 }} />
          </div>
        ) : run.isError ? (
          <div className="banner err mt-4" role="alert">
            {errorText(run.error)}
          </div>
        ) : run.data ? (
          <PreviewResult p={run.data} u={u} />
        ) : null}
      </div>
    </Section>
  );
}
