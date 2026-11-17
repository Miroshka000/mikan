import { Link } from "@tanstack/react-router";
import { ChevronLeft, Dices, Redo2, Undo2 } from "lucide-react";
import { useNodes, useSettings } from "../../../api/hooks";
import { Button, Segmented } from "../../../components/ui";
import { t } from "../../../i18n";
import { IconButton } from "./fields-form";
import { useSaveSite } from "./save";
import { useEditor, type EditorStore } from "./store";

export type View = { preview: boolean; narrow: boolean };

/** The bar over the canvas: the way back, undo and redo, how the canvas shows the page, a random site and saving. */
export function TopBar({ store, view, onView, max }: { store: EditorStore; view: View; onView: (v: View) => void; max: number }) {
  const { doc, canUndo, canRedo, dirty } = useEditor(store);
  const settings = useSettings();
  const nodes = useNodes();
  const save = useSaveSite(store);
  const target = (everywhere: boolean) => ({
    everywhere,
    max,
    panelHas: settings.data?.site_template === "custom",
    nodes: (nodes.data ?? []).map((n) => ({ id: n.id, name: n.name, has: n.front_page === "custom" })),
  });
  return (
    <header className="sb-top">
      <Link to="/settings" search={{ tab: "security" }} className="btn btn-ghost btn-sm" title={t("siteBuilder.back")}>
        <ChevronLeft size={16} aria-hidden /> <span className="sb-wide">{t("siteBuilder.back")}</span>
      </Link>
      <IconButton label={t("siteBuilder.undo")} disabled={!canUndo} onClick={() => store.undo()}>
        <Undo2 size={16} />
      </IconButton>
      <IconButton label={t("siteBuilder.redo")} disabled={!canRedo} onClick={() => store.redo()}>
        <Redo2 size={16} />
      </IconButton>
      <div className="mx-auto flex flex-wrap items-center gap-2">
        <Segmented
          label={t("siteBuilder.mode")}
          value={view.preview ? "preview" : "edit"}
          options={[
            { value: "edit", label: t("siteBuilder.modeEdit") },
            { value: "preview", label: t("siteBuilder.modePreview") },
          ]}
          onChange={(v) => onView({ ...view, preview: v === "preview" })}
        />
        <Segmented
          label={t("siteBuilder.viewport")}
          value={view.narrow ? "phone" : "desktop"}
          options={[
            { value: "desktop", label: t("siteBuilder.desktop") },
            { value: "phone", label: t("siteBuilder.phone") },
          ]}
          onChange={(v) => onView({ ...view, narrow: v === "phone" })}
        />
      </div>
      <span className="sb-wide text-xs text-[var(--ink-500)]">{dirty ? t("siteBuilder.unsaved") : t("siteBuilder.upToDate")}</span>
      <Button size="sm" onClick={() => store.random(doc.lang)}>
        <Dices size={16} aria-hidden /> <span className="sb-wide">{t("siteBuilder.random")}</span>
      </Button>
      <Button size="sm" disabled={save.isPending} loading={save.isPending && save.variables?.everywhere === false} onClick={() => save.mutate(target(false))}>
        {t("siteBuilder.save")}
      </Button>
      <Button size="sm" variant="primary" disabled={save.isPending || !nodes.isSuccess || !settings.isSuccess} loading={save.isPending && save.variables?.everywhere === true} onClick={() => save.mutate(target(true))}>
        {t("siteBuilder.saveEverywhere")}
      </Button>
    </header>
  );
}
