import { ArrowDown, ArrowUp, Copy, Trash2, X } from "lucide-react";
import { t } from "../../../i18n";
import { kindOf } from "../../../lib/site-builder";
import { DesignPanel } from "./design";
import { FieldsForm, IconButton } from "./fields-form";
import { useEditor, type EditorStore } from "./store";

/** The right side: the selected block's fields, or, with nothing selected, the site's design. */
export function Inspector({ store }: { store: EditorStore }) {
  const { selected } = useEditor(store);
  const block = selected ? store.page.blocks.find((b) => b.id === selected) : undefined;
  const kind = block ? kindOf(block) : undefined;
  if (!block || !kind) {
    return (
      <aside className="sb-side sb-right" aria-label={t("siteBuilder.design")}>
        <header className="sb-side-head">
          <h2>{t("siteBuilder.design")}</h2>
        </header>
        <div className="sb-side-body">
          <DesignPanel store={store} />
        </div>
      </aside>
    );
  }
  const index = store.page.blocks.indexOf(block);
  return (
    <aside className="sb-side sb-right" aria-label={t(`siteBuilder.types.${kind.type}` as "siteBuilder.types.hero")}>
      <header className="sb-side-head">
        <h2 className="min-w-0 flex-1 truncate">{t(`siteBuilder.types.${kind.type}` as "siteBuilder.types.hero")}</h2>
        <IconButton label={t("siteBuilder.up")} disabled={index === 0} onClick={() => store.moveBlock(block.id, -1)}>
          <ArrowUp size={16} />
        </IconButton>
        <IconButton label={t("siteBuilder.down")} disabled={index === store.page.blocks.length - 1} onClick={() => store.moveBlock(block.id, 1)}>
          <ArrowDown size={16} />
        </IconButton>
        <IconButton label={t("siteBuilder.duplicate")} onClick={() => store.duplicateBlock(block.id)}>
          <Copy size={16} />
        </IconButton>
        <IconButton label={t("siteBuilder.remove")} onClick={() => store.removeBlock(block.id)}>
          <Trash2 size={16} />
        </IconButton>
        <IconButton label={t("siteBuilder.toDesign")} onClick={() => store.select(null)}>
          <X size={16} />
        </IconButton>
      </header>
      <div className="sb-side-body">
        <FieldsForm store={store} blockId={block.id} data={block} fields={kind.fields} />
      </div>
    </aside>
  );
}
