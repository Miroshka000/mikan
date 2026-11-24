import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useState } from "react";
import { api, unwrap } from "../../../../api/client";
import { qk } from "../../../../api/hooks";
import { Confirm } from "../../../../components/overlay";
import { SwitchRow } from "../../../../components/switch";
import { useToast } from "../../../../components/toast";
import { Button, Field } from "../../../../components/ui";
import { t } from "../../../../i18n";
import { onFormError } from "../../../../lib/fields";
import type { Core, Saved, TemplateRouting } from "./model";

/** The way back from an editor to every template. */
export function BackButton({ onClick }: { onClick: () => void }) {
  return (
    <div>
      <Button size="sm" variant="ghost" onClick={onClick}>
        <ArrowLeft size={16} aria-hidden /> {t("settings.subTemplates.back")}
      </Button>
    </div>
  );
}

/**
 * The guard of an editor's unsaved changes: back asks first while the editor is dirty, and so
 * does leaving the page; nothing is asked while they are being saved. dialog is the question,
 * for the editor to show.
 */
export function useLeaveGuard(dirty: boolean, saving: boolean, onClose: () => void) {
  const [leaving, setLeaving] = useState(false);
  const leave = useBlocker({ shouldBlockFn: () => dirty && !saving, enableBeforeUnload: () => dirty, withResolver: true });
  return {
    back: () => (dirty ? setLeaving(true) : onClose()),
    dialog: (
      <Confirm
        open={leaving || leave.status === "blocked"}
        onOpenChange={(o) => {
          if (o) return;
          setLeaving(false);
          leave.reset?.();
        }}
        title={t("settings.page.leaveTitle")}
        text={t("settings.subTemplates.leaveText")}
        confirm={t("settings.page.leaveConfirm")}
        danger
        onConfirm={() => {
          if (leave.status === "blocked") {
            leave.proceed?.();
            return;
          }
          setLeaving(false);
          onClose();
        }}
      />
    ),
  };
}

/** What a save sends of a template's content: its text or its routing; neither where it is hidden. */
type Content = { body: string } | { routing: TemplateRouting } | Record<string, never>;

/**
 * The save of an editor: a change of the saved template, or a new one of core. A refusal that
 * names fields is shown under them and anything else is a toast; onRefused hears of it too.
 */
export function useSaveTemplate(saved: Saved | undefined, core: Core, onSaved: (t: Saved) => void, onRefused?: (e: unknown) => void) {
  const qc = useQueryClient();
  const toast = useToast();
  return useMutation({
    mutationFn: ({ name, def, content }: { name: string; def: boolean; content: Content }) =>
      saved
        ? unwrap(api.PATCH("/api/v1/sub-templates/{id}", { params: { path: { id: saved.id } }, body: { name, default: def, ...content } }))
        : unwrap(api.POST("/api/v1/sub-templates", { body: { core, name, default: def, ...content } })),
    onSuccess: (next) => {
      qc.setQueryData(qk.subTemplate(next.id), next);
      void qc.invalidateQueries({ queryKey: qk.subTemplates });
      toast.ok(saved ? t("settings.subTemplates.saved") : t("settings.subTemplates.created"));
      onSaved(next);
    },
    onError: (e) => {
      onFormError(e, toast);
      onRefused?.(e);
    },
  });
}

/** A template's name, seen in the panel only, and whether it is its core's default; defaultSub says who gets it then. */
export function NameFields({
  name,
  setName,
  def,
  setDef,
  error,
  defaultSub,
}: {
  name: string;
  setName: (v: string) => void;
  def: boolean;
  setDef: (v: boolean) => void;
  error?: string;
  defaultSub: string;
}) {
  return (
    <>
      <Field label={t("settings.subTemplates.name")} htmlFor="sub-template-name" hint={t("settings.subTemplates.nameHint")} error={error}>
        <input id="sub-template-name" className="input" value={name} maxLength={80} aria-invalid={!!error} onChange={(e) => setName(e.target.value)} />
      </Field>
      <SwitchRow label={t("settings.subTemplates.defaultSwitch")} sub={defaultSub} checked={def} onChange={setDef} />
    </>
  );
}
