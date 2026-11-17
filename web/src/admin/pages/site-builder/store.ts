import { useSyncExternalStore } from "react";
import { freshSeed, kindByType, newId, SiteGenerator, slug, type BlockData, type Kind, type Lang, type Page, type SiteDoc, type SiteImage } from "../../../lib/site-builder";
import { setAt } from "./paths";

export type EditorState = {
  doc: SiteDoc;
  /** The page on the canvas. */
  pageId: string;
  /** The block the inspector shows; null: the site's design. */
  selected: string | null;
  /** Grows with every change the canvas has to draw again; typing on the canvas itself does not move it. */
  drawn: number;
  canUndo: boolean;
  canRedo: boolean;
  /** The document differs from the one the panel keeps. */
  dirty: boolean;
};

const HISTORY = 80;

/**
 * The editor's state and everything that changes it: one object the canvas, the panels and
 * the top bar all hold. A change replaces the document (nothing is edited in place), so the
 * history is a list of documents and undo is a step back in it.
 */
export class EditorStore {
  private state: EditorState;
  private past: SiteDoc[] = [];
  private future: SiteDoc[] = [];
  /** What the last change was about: typing in one text is one step of the history. */
  private typing = "";
  private readonly listeners = new Set<() => void>();
  private readonly generator = new SiteGenerator();
  /** The document as the panel keeps it; undefined: nothing of this editor is saved yet. */
  private saved?: SiteDoc;

  constructor(
    doc: SiteDoc,
    /** The installation's seed: every fresh seed of this editor starts with it. */
    private readonly installation: string,
    /** The document came from the panel: it is the saved one until something changes. */
    fromPanel: boolean,
  ) {
    if (fromPanel) this.saved = doc;
    this.state = { doc, pageId: doc.pages[0]!.id, selected: null, drawn: 0, canUndo: false, canRedo: false, dirty: !fromPanel };
  }

  /** The panel keeps this document now. */
  markSaved(doc: SiteDoc) {
    this.saved = doc;
    this.emit({});
  }

  subscribe = (f: () => void) => {
    this.listeners.add(f);
    return () => this.listeners.delete(f);
  };
  snapshot = () => this.state;

  get doc(): SiteDoc {
    return this.state.doc;
  }
  get page(): Page {
    return this.state.doc.pages.find((p) => p.id === this.state.pageId) ?? this.state.doc.pages[0]!;
  }

  private emit(patch: Partial<EditorState>) {
    this.state = { ...this.state, ...patch, canUndo: this.past.length > 0, canRedo: this.future.length > 0 };
    this.state.dirty = this.state.doc !== this.saved;
    this.listeners.forEach((f) => f());
  }

  /** Replaces the document. `typing` names a text being typed: its changes are one step back, and with `quiet` the canvas, where the text already is, is left as it is. */
  private change(doc: SiteDoc, opts: { typing?: string; quiet?: boolean; patch?: Partial<EditorState> } = {}) {
    if (!opts.typing || opts.typing !== this.typing) {
      this.past.push(this.state.doc);
      if (this.past.length > HISTORY) this.past.shift();
    }
    this.typing = opts.typing ?? "";
    this.future = [];
    const pageId = doc.pages.some((p) => p.id === (opts.patch?.pageId ?? this.state.pageId)) ? (opts.patch?.pageId ?? this.state.pageId) : doc.pages[0]!.id;
    this.emit({ ...opts.patch, doc, pageId, drawn: this.state.drawn + (opts.quiet ? 0 : 1) });
  }

  undo() {
    const doc = this.past.pop();
    if (!doc) return;
    this.future.push(this.state.doc);
    this.restore(doc);
  }
  redo() {
    const doc = this.future.pop();
    if (!doc) return;
    this.past.push(this.state.doc);
    this.restore(doc);
  }
  private restore(doc: SiteDoc) {
    this.typing = "";
    const pageId = doc.pages.some((p) => p.id === this.state.pageId) ? this.state.pageId : doc.pages[0]!.id;
    const selected = doc.pages.some((p) => p.blocks.some((b) => b.id === this.state.selected)) ? this.state.selected : null;
    this.emit({ doc, pageId, selected, drawn: this.state.drawn + 1 });
  }

  select(id: string | null) {
    this.typing = "";
    if (id !== this.state.selected) this.emit({ selected: id });
  }
  openPage(id: string) {
    this.typing = "";
    if (id !== this.state.pageId && this.doc.pages.some((p) => p.id === id)) this.emit({ pageId: id, selected: null, drawn: this.state.drawn + 1 });
  }

  // --- the site

  /** A whole new site at random. */
  random(lang: Lang, kind?: Kind) {
    const doc = this.generator.random(freshSeed(this.installation), lang, kind);
    this.change(doc, { patch: { pageId: doc.pages[0]!.id, selected: null } });
  }
  /** The same words in other markup, spacing and drawings. */
  relook() {
    this.change({ ...this.doc, seed: freshSeed(this.installation) });
  }
  setSite(patch: Partial<Pick<SiteDoc, "brand" | "tag" | "foot" | "theme">>, opts: { typing?: string; quiet?: boolean } = {}) {
    this.change({ ...this.doc, ...patch }, opts);
  }
  setTheme(patch: Partial<SiteDoc["theme"]>) {
    this.change({ ...this.doc, theme: { ...this.doc.theme, ...patch } }, { typing: "theme:" + Object.keys(patch).join() });
  }

  // --- pages

  private withPages(pages: Page[], patch?: Partial<EditorState>, opts: { typing?: string } = {}) {
    this.change({ ...this.doc, pages }, { ...opts, patch });
  }
  addPage(title: string) {
    const taken = new Set(this.doc.pages.map((p) => p.path));
    let path = slug(title) || "page";
    for (let n = 2; taken.has(path); n++) path = `${slug(title) || "page"}-${n}`;
    const page: Page = { id: newId("p"), path, title, blocks: [] };
    this.withPages([...this.doc.pages, page], { pageId: page.id, selected: null });
  }
  /** A page's name in the menu, or its address: the first page keeps the address "". */
  setPage(id: string, patch: { title?: string; path?: string }) {
    const pages = this.doc.pages.map((p, i) => {
      if (p.id !== id) return p;
      const path = patch.path === undefined || i === 0 ? p.path : slug(patch.path);
      const free = path && !this.doc.pages.some((x) => x.id !== id && x.path === path) ? path : p.path;
      return { ...p, title: patch.title ?? p.title, path: free };
    });
    this.withPages(pages, undefined, { typing: `page:${id}:${Object.keys(patch).join()}` });
  }
  removePage(id: string) {
    if (this.doc.pages[0]!.id === id) return;
    this.withPages(this.doc.pages.filter((p) => p.id !== id), { pageId: this.doc.pages[0]!.id, selected: null });
  }
  movePage(id: string, by: number) {
    const pages = [...this.doc.pages];
    const i = pages.findIndex((p) => p.id === id);
    const j = i + by;
    // The first page is the first: it has the address "/".
    if (i <= 0 || j <= 0 || j >= pages.length) return;
    [pages[i], pages[j]] = [pages[j]!, pages[i]!];
    this.withPages(pages);
  }

  // --- blocks of the page on the canvas

  private withBlocks(map: (blocks: BlockData[]) => BlockData[], opts: { typing?: string; quiet?: boolean; patch?: Partial<EditorState> } = {}) {
    const pageId = this.page.id;
    this.change({ ...this.doc, pages: this.doc.pages.map((p) => (p.id === pageId ? { ...p, blocks: map(p.blocks) } : p)) }, opts);
  }
  /** A new block of type after the selected one, or at the end. */
  addBlock(type: string) {
    const kind = kindByType(type);
    if (!kind) return;
    const block = kind.create(this.doc.lang);
    const at = this.page.blocks.findIndex((b) => b.id === this.state.selected);
    this.withBlocks((blocks) => (at < 0 ? [...blocks, block] : [...blocks.slice(0, at + 1), block, ...blocks.slice(at + 1)]), { patch: { selected: block.id } });
  }
  removeBlock(id: string) {
    this.withBlocks((blocks) => blocks.filter((b) => b.id !== id), { patch: { selected: null } });
  }
  duplicateBlock(id: string) {
    const copy = { ...(JSON.parse(JSON.stringify(this.page.blocks.find((b) => b.id === id) ?? {})) as BlockData), id: newId() };
    if (!copy.t) return;
    this.withBlocks((blocks) => blocks.flatMap((b) => (b.id === id ? [b, copy] : [b])), { patch: { selected: copy.id } });
  }
  moveBlock(id: string, by: number) {
    this.withBlocks((blocks) => {
      const out = [...blocks];
      const i = out.findIndex((b) => b.id === id);
      const j = i + by;
      if (i < 0 || j < 0 || j >= out.length) return blocks;
      [out[i], out[j]] = [out[j]!, out[i]!];
      return out;
    });
  }
  /** A value inside a block; `quiet` when the canvas already shows it (typed there). */
  setIn(id: string, path: string, value: unknown, opts: { quiet?: boolean } = {}) {
    this.withBlocks((blocks) => blocks.map((b) => (b.id === id ? setAt(b, path, value) : b)), { typing: `${id}:${path}`, quiet: opts.quiet });
  }

  // --- pictures

  /** Keeps a picture and gives its id. */
  addImage(image: SiteImage): string {
    const id = newId("i");
    this.change({ ...this.doc, images: { ...this.doc.images, [id]: image } }, { quiet: true });
    return id;
  }
}

export function useEditor(store: EditorStore): EditorState {
  return useSyncExternalStore(store.subscribe, store.snapshot);
}
