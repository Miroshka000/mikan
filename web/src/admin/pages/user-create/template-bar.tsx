import { Trash2 } from "lucide-react";
import { useState } from "react";
import { errorText } from "../../../api/client";
import { userTemplateActions, useUserTemplateMutation, useUserTemplates } from "../../../api/hooks";
import { Confirm, NameDialog } from "../../../components/overlay";
import { useToast } from "../../../components/toast";
import { Button, Skeleton } from "../../../components/ui";
import { t } from "../../../i18n";
import { draftDiffers, templateBody, type Draft, type Template } from "./model";

/** As long as the API takes a template's name. */
const NAME_MAX = 60;

/**
 * The templates of clients over the form: picking one fills the form, which is then saved over
 * the template picked or as a new one. A template only fills the form: a client made from it
 * keeps nothing of it, and changing the template changes no client made before.
 */
export function TemplateBar({ picked, onPick, draft }: { picked: Template | null; onPick: (tpl: Template | null) => void; draft: Draft }) {
  const templates = useUserTemplates();
  const toast = useToast();
  const create = useUserTemplateMutation(userTemplateActions.create);
  const update = useUserTemplateMutation(userTemplateActions.update);
  const remove = useUserTemplateMutation(userTemplateActions.remove);
  const [naming, setNaming] = useState(false);
  const [removing, setRemoving] = useState(false);
  const list = templates.data ?? [];
  // A template names a tariff: there is nothing to save before one is chosen.
  const ready = draft.tariffId !== undefined;
  const saveOver = () => {
    if (!picked) return;
    update.mutate(
      { id: picked.id, body: templateBody(draft, picked.name) },
      {
        onSuccess: (tpl) => {
          onPick(tpl);
          toast.ok(t("userCreate.templateUpdated", { name: tpl.name }));
        },
        onError: (e) => toast.error(errorText(e)),
      },
    );
  };
  return (
    <section className="flex flex-col gap-2" aria-label={t("userCreate.templates")}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-[13px] font-semibold text-[var(--ink-700)]">{t("userCreate.templates")}</span>
        <div className="flex flex-wrap items-center gap-1">
          {picked && draftDiffers(draft, picked) ? (
            <Button size="sm" variant="ghost" loading={update.isPending} disabled={!ready} onClick={saveOver}>
              {t("userCreate.templateUpdate", { name: picked.name })}
            </Button>
          ) : null}
          <Button size="sm" variant="ghost" disabled={!ready} onClick={() => setNaming(true)}>
            {picked ? t("userCreate.templateSaveNew") : t("userCreate.templateSave")}
          </Button>
          {picked ? (
            <Button size="sm" variant="danger" aria-label={t("userCreate.templateRemove", { name: picked.name })} title={t("userCreate.templateRemove", { name: picked.name })} onClick={() => setRemoving(true)}>
              <Trash2 size={16} aria-hidden />
            </Button>
          ) : null}
        </div>
      </div>
      {templates.isPending ? (
        <Skeleton style={{ height: 36, borderRadius: 999 }} />
      ) : list.length === 0 ? (
        <p className="text-xs text-[var(--ink-500)]">{t("userCreate.templatesEmpty")}</p>
      ) : (
        <div className="flex flex-wrap gap-2" role="group" aria-label={t("userCreate.templates")}>
          <button type="button" className="chip" aria-pressed={picked === null} onClick={() => onPick(null)}>
            {t("userCreate.noTemplate")}
          </button>
          {list.map((tpl) => (
            <button key={tpl.id} type="button" className="chip" aria-pressed={picked?.id === tpl.id} onClick={() => onPick(tpl)}>
              <span className="max-w-[200px] truncate">{tpl.name}</span>
            </button>
          ))}
        </div>
      )}
      <NameDialog
        open={naming}
        onOpenChange={(o) => {
          setNaming(o);
          if (!o) create.reset();
        }}
        title={t("userCreate.templateSaveTitle")}
        label={t("userCreate.templateName")}
        hint={t("userCreate.templateNameHint")}
        initial=""
        placeholder={t("userCreate.templateNamePlaceholder")}
        maxLength={NAME_MAX}
        loading={create.isPending}
        error={create.error ? errorText(create.error) : undefined}
        onSubmit={(name) =>
          create.mutate(templateBody(draft, name), {
            onSuccess: (tpl) => {
              setNaming(false);
              onPick(tpl);
              toast.ok(t("userCreate.templateSaved", { name: tpl.name }));
            },
          })
        }
      />
      {picked ? (
        <Confirm
          open={removing}
          onOpenChange={setRemoving}
          title={t("userCreate.templateRemoveTitle")}
          text={t("userCreate.templateRemoveText", { name: picked.name })}
          confirm={t("common.delete")}
          danger
          loading={remove.isPending}
          onConfirm={() =>
            remove.mutate(picked.id, {
              onSuccess: () => {
                setRemoving(false);
                onPick(null);
                toast.ok(t("userCreate.templateRemoved"));
              },
              onError: (e) => toast.error(errorText(e)),
            })
          }
        />
      ) : null}
    </section>
  );
}
