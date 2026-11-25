import { useMutation, useQueryClient } from "@tanstack/react-query";
import { FileCode2, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, errorText, unwrap } from "../../../../api/client";
import { qk, useGroups } from "../../../../api/hooks";
import { Card } from "../../../../components/layout";
import { Confirm } from "../../../../components/overlay";
import { useToast } from "../../../../components/toast";
import { Button, EmptyState, Pill } from "../../../../components/ui";
import { t, useLocale } from "../../../../i18n";
import { bytes, dateShort } from "../../../../lib/format";
import { CORES, coreName, groupsPicking, hasRoom, kindName, type Head, type View } from "./model";

/**
 * Every template, by core in the order they were made: what it is written as, its core, the
 * default of each core, and the response rules and groups of clients that pick it; a template
 * opens in its editor, and is made the default or deleted from here.
 */
export function TemplateList({ v, onOpen, onNew }: { v: View; onOpen: (h: Head) => void; onNew: () => void }) {
  useLocale();
  const qc = useQueryClient();
  const toast = useToast();
  const groups = useGroups();
  const picking = groupsPicking(groups.data ?? []);
  const [removing, setRemoving] = useState<Head | null>(null);
  const setDefault = useMutation({
    mutationFn: ({ id, on }: { id: number; on: boolean }) => unwrap(api.PATCH("/api/v1/sub-templates/{id}", { params: { path: { id } }, body: { default: on } })),
    onSuccess: (next) => {
      qc.setQueryData(qk.subTemplate(next.id), next);
      void qc.invalidateQueries({ queryKey: qk.subTemplates });
      toast.ok(next.default ? t("settings.subTemplates.madeDefault") : t("settings.subTemplates.unsetDone"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const remove = useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/api/v1/sub-templates/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      setRemoving(null);
      void qc.invalidateQueries({ queryKey: qk.subTemplates });
      // A group that picked the template has the core's default again.
      void qc.invalidateQueries({ queryKey: qk.groups });
      toast.ok(t("settings.subTemplates.removed"));
    },
    onError: (e) => {
      setRemoving(null);
      toast.error(errorText(e));
    },
  });
  const full = CORES.filter((c) => !hasRoom(v, c));
  const create = (
    <Button variant="primary" size="sm" disabled={full.length === CORES.length} onClick={onNew}>
      <Plus size={16} aria-hidden /> {t("settings.subTemplates.new")}
    </Button>
  );
  return (
    <Card title={t("settings.subTemplates.title")} sub={t("settings.subTemplates.lead")} i={2} aside={v.templates.length > 0 ? create : undefined}>
      {v.templates.length === 0 ? (
        <EmptyState icon={FileCode2} title={t("settings.subTemplates.emptyTitle")} text={t("settings.subTemplates.emptyText")}>
          {create}
        </EmptyState>
      ) : (
        <ul className="flex flex-col gap-2">
          {v.templates.map((x) => {
            const by = picking.get(x.id) ?? [];
            return (
              <li key={x.id} className="panel-soft flex flex-wrap items-center gap-3 p-3">
                <button type="button" className="min-w-0 flex-1 basis-48 text-left" onClick={() => onOpen(x)}>
                  <span className="flex min-w-0 flex-wrap items-center gap-2">
                    <span className="truncate text-[14px] font-medium">{x.name}</span>
                    <Pill tone="off">{kindName(x)}</Pill>
                    <span className="text-xs text-[var(--ink-500)]">{x.kind === "simple" ? t("settings.subTemplates.forAllApps") : coreName(x.core)}</span>
                    {x.default ? <Pill tone="ok">{x.kind === "simple" ? t("settings.subTemplates.defaultOf", { core: coreName(x.core) }) : t("settings.subTemplates.default")}</Pill> : null}
                  </span>
                  <span className="block text-xs text-[var(--ink-500)]">
                    {x.kind === "simple" ? t("settings.subTemplates.metaSimple", { date: dateShort(x.updated_at) }) : t("settings.subTemplates.meta", { size: bytes(x.size), date: dateShort(x.updated_at) })}
                  </span>
                  {x.rules.length > 0 ? <span className="block text-xs break-words text-[var(--ink-500)]">{t("settings.subTemplates.usedBy", { rules: x.rules.join(", ") })}</span> : null}
                  {by.length > 0 ? <span className="block text-xs break-words text-[var(--ink-500)]">{t("settings.subTemplates.usedByGroups", { groups: by.join(", ") })}</span> : null}
                </button>
                <div className="flex flex-wrap items-center gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    loading={setDefault.isPending && setDefault.variables?.id === x.id}
                    disabled={setDefault.isPending && setDefault.variables?.id !== x.id}
                    onClick={() => setDefault.mutate({ id: x.id, on: !x.default })}
                  >
                    {x.default ? t("settings.subTemplates.unsetDefault") : t("settings.subTemplates.makeDefault")}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => onOpen(x)}>
                    {t("settings.subTemplates.edit")}
                  </Button>
                  <Button size="sm" variant="danger" aria-label={t("settings.subTemplates.remove", { name: x.name })} title={t("settings.subTemplates.remove", { name: x.name })} onClick={() => setRemoving(x)}>
                    <Trash2 size={16} aria-hidden />
                  </Button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
      {full.length > 0 ? <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.subTemplates.limit", { max: v.max, cores: full.map(coreName).join(", ") })}</p> : null}
      <Confirm
        open={removing !== null}
        onOpenChange={(o) => !o && setRemoving(null)}
        title={t("settings.subTemplates.removeTitle")}
        text={removing ? `${t("settings.subTemplates.removeText", { name: removing.name })} ${removing.default ? t("settings.subTemplates.removeDefault") : ""}`.trim() : ""}
        confirm={t("common.delete")}
        danger
        loading={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing.id)}
      />
    </Card>
  );
}
