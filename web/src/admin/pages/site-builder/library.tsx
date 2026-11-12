import { ArrowDown, ArrowUp, Dices, Plus, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import { Select } from "../../../components/select";
import { Button, Field, Segmented } from "../../../components/ui";
import { t } from "../../../i18n";
import { BLOCK_KINDS, kindOf, KINDS, type BlockGroup, type Kind, type Lang } from "../../../lib/site-builder";
import { IconButton } from "./fields-form";
import { useEditor, type EditorStore } from "./store";

type Tab = "blocks" | "structure" | "pages" | "site";
const TABS: Tab[] = ["blocks", "structure", "pages", "site"];
const GROUPS: BlockGroup[] = ["structure", "content", "media", "business"];
const typeName = (type: string) => t(`siteBuilder.types.${type}` as "siteBuilder.types.hero");

/** The left side: the blocks to add, the blocks of the page, the pages of the site, and a whole new site. */
export function Library({ store }: { store: EditorStore }) {
  const [tab, setTab] = useState<Tab>("blocks");
  return (
    <aside className="sb-side sb-left" aria-label={t("siteBuilder.library")}>
      <header className="sb-side-head">
        <div className="sb-tabs" role="tablist" aria-label={t("siteBuilder.library")}>
          {TABS.map((x) => (
            <button key={x} type="button" role="tab" aria-selected={tab === x} onClick={() => setTab(x)}>
              {t(`siteBuilder.tab.${x}`)}
            </button>
          ))}
        </div>
      </header>
      <div className="sb-side-body">{tab === "blocks" ? <Blocks store={store} /> : tab === "structure" ? <Structure store={store} /> : tab === "pages" ? <Pages store={store} /> : <Site store={store} />}</div>
    </aside>
  );
}

/** Every kind of block, by group, with a search: a press adds one after the selected block. */
function Blocks({ store }: { store: EditorStore }) {
  const [q, setQ] = useState("");
  const needle = q.trim().toLowerCase();
  const shown = BLOCK_KINDS.filter((k) => !needle || `${typeName(k.type)} ${t(`siteBuilder.about.${k.type}` as "siteBuilder.about.hero")}`.toLowerCase().includes(needle));
  return (
    <>
      <label className="search-field mb-3">
        <Search size={16} aria-hidden />
        <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("siteBuilder.findBlock")} aria-label={t("siteBuilder.findBlock")} />
      </label>
      {shown.length === 0 ? <p className="sb-note">{t("siteBuilder.noBlockFound")}</p> : null}
      {GROUPS.map((g) => {
        const kinds = shown.filter((k) => k.group === g);
        return kinds.length === 0 ? null : (
          <section key={g} className="mb-4">
            <h3 className="sb-h mb-2">{t(`siteBuilder.group.${g}`)}</h3>
            <div className="flex flex-col gap-2">
              {kinds.map((k) => (
                <button key={k.type} type="button" className="sb-kind" onClick={() => store.addBlock(k.type)}>
                  <span className="font-medium">{typeName(k.type)}</span>
                  <span className="text-xs text-[var(--ink-500)]">{t(`siteBuilder.about.${k.type}` as "siteBuilder.about.hero")}</span>
                </button>
              ))}
            </div>
          </section>
        );
      })}
    </>
  );
}

/** The blocks of the page on the canvas, in their order. */
function Structure({ store }: { store: EditorStore }) {
  const { selected } = useEditor(store);
  const blocks = store.page.blocks;
  if (blocks.length === 0) return <p className="sb-note">{t("siteBuilder.noBlocks")}</p>;
  return (
    <ol className="flex flex-col gap-1">
      {blocks.map((b, i) => (
        <li key={b.id} className="sb-line" data-on={b.id === selected || undefined}>
          <button type="button" className="min-w-0 flex-1 truncate text-left" onClick={() => store.select(b.id)}>
            <span className="text-[var(--ink-500)]">{kindOf(b) ? typeName(b.t) : b.t}</span> {typeof b.title === "string" ? b.title : ""}
          </button>
          <IconButton label={t("siteBuilder.up")} disabled={i === 0} onClick={() => store.moveBlock(b.id, -1)}>
            <ArrowUp size={16} />
          </IconButton>
          <IconButton label={t("siteBuilder.down")} disabled={i === blocks.length - 1} onClick={() => store.moveBlock(b.id, 1)}>
            <ArrowDown size={16} />
          </IconButton>
          <IconButton label={t("siteBuilder.remove")} onClick={() => store.removeBlock(b.id)}>
            <Trash2 size={16} />
          </IconButton>
        </li>
      ))}
    </ol>
  );
}

/** The pages of the site: the one on the canvas, each one's name and address, a new one. */
function Pages({ store }: { store: EditorStore }) {
  const { doc, pageId } = useEditor(store);
  const [title, setTitle] = useState("");
  return (
    <>
      <ol className="mb-4 flex flex-col gap-2">
        {doc.pages.map((p, i) => (
          <li key={p.id} className="sb-page" data-on={p.id === pageId || undefined}>
            <div className="flex items-center gap-1">
              <button type="button" className="min-w-0 flex-1 truncate text-left font-medium" onClick={() => store.openPage(p.id)}>
                {p.title || t("siteBuilder.untitled")}
              </button>
              <IconButton label={t("siteBuilder.up")} disabled={i <= 1} onClick={() => store.movePage(p.id, -1)}>
                <ArrowUp size={16} />
              </IconButton>
              <IconButton label={t("siteBuilder.down")} disabled={i === 0 || i === doc.pages.length - 1} onClick={() => store.movePage(p.id, 1)}>
                <ArrowDown size={16} />
              </IconButton>
              <IconButton label={t("siteBuilder.remove")} disabled={i === 0} onClick={() => store.removePage(p.id)}>
                <Trash2 size={16} />
              </IconButton>
            </div>
            {p.id === pageId ? (
              <div className="mt-2 grid gap-2">
                <input className="input" aria-label={t("siteBuilder.pageTitle")} value={p.title} maxLength={40} onChange={(e) => store.setPage(p.id, { title: e.target.value })} />
                {i === 0 ? (
                  <span className="text-xs text-[var(--ink-500)]">{t("siteBuilder.pageFirst")}</span>
                ) : (
                  <label className="flex items-center gap-1 text-[13px] text-[var(--ink-500)]">
                    /
                    <input className="input mono" aria-label={t("siteBuilder.pagePath")} defaultValue={p.path} key={p.path} maxLength={40} onBlur={(e) => store.setPage(p.id, { path: e.target.value })} />
                  </label>
                )}
              </div>
            ) : (
              <span className="mono text-xs text-[var(--ink-500)]">/{p.path}</span>
            )}
          </li>
        ))}
      </ol>
      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (!title.trim()) return;
          store.addPage(title.trim());
          setTitle("");
        }}
      >
        <div className="min-w-0 flex-1">
          <Field label={t("siteBuilder.newPage")} htmlFor="sb-new-page">
            <input id="sb-new-page" className="input" value={title} maxLength={40} placeholder={t("siteBuilder.newPageHint")} onChange={(e) => setTitle(e.target.value)} />
          </Field>
        </div>
        <Button type="submit" disabled={!title.trim() || doc.pages.length >= 12}>
          <Plus size={16} aria-hidden /> {t("siteBuilder.add")}
        </Button>
      </form>
    </>
  );
}

/** A whole new site: its kind and language, and "random" that draws one. */
function Site({ store }: { store: EditorStore }) {
  const { doc } = useEditor(store);
  const [kind, setKind] = useState<Kind | "">("");
  return (
    <div className="flex flex-col gap-3">
      <p className="sb-note">{t("siteBuilder.randomHint")}</p>
      <Field label={t("siteBuilder.kind")} htmlFor="sb-kind">
        <Select id="sb-kind" className="input" value={kind} onChange={(e) => setKind(e.target.value as Kind | "")}>
          <option value="">{t("siteBuilder.kindAny")}</option>
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {t(`siteBuilder.kinds.${k}`)}
            </option>
          ))}
        </Select>
      </Field>
      <Field label={t("siteBuilder.lang")}>
        <Segmented
          label={t("siteBuilder.lang")}
          value={doc.lang}
          options={[
            { value: "ru", label: "Русский" },
            { value: "en", label: "English" },
          ]}
          onChange={(l) => l !== doc.lang && store.random(l as Lang, kind || doc.kind)}
        />
      </Field>
      <Button variant="primary" onClick={() => store.random(doc.lang, kind || undefined)}>
        <Dices size={16} aria-hidden /> {t("siteBuilder.random")}
      </Button>
      <Button onClick={() => store.relook()} title={t("siteBuilder.relookHint")}>
        {t("siteBuilder.relook")}
      </Button>
      <p className="sb-note">{t("siteBuilder.langHint")}</p>
    </div>
  );
}
