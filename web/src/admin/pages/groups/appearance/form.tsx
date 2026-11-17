import { useId, type FormEvent, type ReactNode } from "react";
import { groupActions, useGroupMutation } from "../../../../api/hooks";
import { FormActions } from "../../../../components/layout";
import { useToast } from "../../../../components/toast";
import { t } from "../../../../i18n";
import { useDraft } from "../../../../lib/draft";
import { fieldErrors, onFormError } from "../../../../lib/fields";
import { ResponseHeaders } from "../../settings/response-headers";
import type { Group } from "../model";
import { AddressesFields } from "./addresses";
import { DevicesFields } from "./devices";
import { appearanceBody, appearanceDraft, draftValid, type AppearanceDraft } from "./model";
import { PageCard } from "./page";
import { TemplatesFields } from "./templates";
import { TextsFields } from "./texts";

// The most headers of the group's own: the API's bound, a response rule's.
const MAX_HEADERS = 10;

/**
 * How the group's subscriptions look (#290): the templates, the texts and headers the apps
 * show, the rules of devices and the addresses, saved together over the panel's settings; the
 * page saves on its own. Anything left as the panel's follows the panel's settings.
 */
export function AppearanceForm({ group }: { group: Group }) {
  const toast = useToast();
  const update = useGroupMutation(groupActions.update);
  const { draft, setDraft, dirty, reset } = useDraft(appearanceDraft(group.appearance));
  const errors = fieldErrors(update.error);
  const set = (patch: Partial<AppearanceDraft>) => setDraft((d) => ({ ...d, ...patch }));
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!draftValid(draft) || update.isPending) return;
    update.mutate(
      { id: group.id, body: { appearance: appearanceBody(draft) } },
      {
        onSuccess: (g) => {
          setDraft(appearanceDraft(g.appearance));
          toast.ok(t("groups.appearance.saved"));
        },
        onError: (e) => onFormError(e, toast),
      },
    );
  };
  return (
    <div className="flex flex-col gap-6">
      <p className="text-[13px] text-[var(--ink-500)]">{t("groups.appearance.intro")}</p>
      <form onSubmit={submit} noValidate className="flex flex-col gap-6">
        <Part title={t("groups.appearance.templates")} sub={t("groups.appearance.templatesSub")}>
          <TemplatesFields value={draft.templates} onChange={(templates) => set({ templates })} errors={errors} />
        </Part>
        <Part title={t("groups.appearance.texts")} sub={t("groups.appearance.textsSub")}>
          <TextsFields draft={draft} set={set} errors={errors} />
        </Part>
        <Part title={t("groups.appearance.headers")} sub={t("groups.appearance.headersSub")}>
          <ResponseHeaders
            headers={draft.headers}
            onChange={(headers) => set({ headers })}
            errorOf={(i, part) => errors[`appearance.headers[${i}].${part}`]}
            listError={errors["appearance.headers"]}
            max={MAX_HEADERS}
          />
        </Part>
        <Part title={t("groups.appearance.devices")} sub={t("groups.appearance.devicesSub")}>
          <DevicesFields draft={draft} set={set} errors={errors} />
        </Part>
        <Part title={t("groups.appearance.addresses")} sub={t("groups.appearance.addressesSub")}>
          <AddressesFields value={draft.addresses} onChange={(addresses) => set({ addresses })} errors={errors} />
        </Part>
        <FormActions dirty={dirty} saving={update.isPending} onReset={reset} disabled={!dirty || !draftValid(draft)} />
      </form>
      <Part title={t("groups.appearance.page")} sub={t("groups.appearance.pageSub")}>
        <PageCard group={group} />
      </Part>
    </div>
  );
}

function Part({ title, sub, children }: { title: string; sub: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id}>
      <h4 id={id} className="text-[13px] font-semibold text-[var(--ink-700)]">
        {title}
      </h4>
      <p className="mt-1 mb-3 text-xs text-[var(--ink-500)]">{sub}</p>
      {children}
    </section>
  );
}
