import { useEffect, useMemo, useRef, useState } from "react";
import { t } from "../../../i18n";
import { SiteRenderer } from "../../../lib/site-builder";
import { canvasAssets } from "./assets";
import { useEditor, type EditorStore } from "./store";

/** What the editor draws over the page: which block the pointer is on, which one is selected, which text is being typed. */
const EDIT_STYLE = `
[data-b]{cursor:pointer;outline:1px dashed transparent;outline-offset:-1px;transition:outline-color .12s}
[data-b]:hover{outline-color:rgba(59,130,246,.55)}
[data-b].sb-on{outline:2px solid #3b82f6;outline-offset:-2px}
[data-e],[data-s]{cursor:text;border-radius:3px;transition:background-color .12s}
[data-e]:hover,[data-s]:hover{background:rgba(59,130,246,.1)}
[contenteditable]{outline:2px solid #3b82f6;outline-offset:2px;background:rgba(59,130,246,.06)}
a{cursor:pointer}
`;

const BLANK = "<!doctype html><html><head><meta charset='utf-8'></head><body></body></html>";

/**
 * The page as a visitor will get it, in a frame of its own. No script runs inside: the
 * editor listens to the frame's document from here. A click selects a block, a click on a
 * text lets it be typed in place, a click on the menu opens that page.
 */
export function Canvas({ store, narrow, preview }: { store: EditorStore; narrow: boolean; preview: boolean }) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [ready, setReady] = useState(false);
  const { doc, pageId, selected, drawn } = useEditor(store);
  // `drawn` moves when the canvas has to draw again; a text typed on the canvas is already there.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const html = useMemo(() => new SiteRenderer(store.doc, { assets: canvasAssets, edit: !preview }).page(store.page), [drawn, pageId, preview, store]);

  // The page into the frame, keeping how far it was scrolled.
  useEffect(() => {
    const d = frame.current?.contentDocument;
    if (!ready || !d) return;
    const y = d.documentElement.scrollTop;
    const parsed = new DOMParser().parseFromString(html, "text/html");
    d.documentElement.lang = parsed.documentElement.lang;
    d.head.innerHTML = parsed.head.innerHTML + (preview ? "" : `<style>${EDIT_STYLE}</style>`);
    d.body.innerHTML = parsed.body.innerHTML;
    d.documentElement.scrollTop = y;
  }, [html, ready, preview]);

  // A page opened from the menu starts at its top.
  useEffect(() => {
    const d = frame.current?.contentDocument;
    if (d) d.documentElement.scrollTop = 0;
  }, [pageId]);

  // The selected block is marked, and brought into view when it was chosen in the panel.
  useEffect(() => {
    const d = frame.current?.contentDocument;
    if (!ready || !d) return;
    d.querySelectorAll(".sb-on").forEach((el) => el.classList.remove("sb-on"));
    if (!selected || preview) return;
    const el = d.querySelector<HTMLElement>(`[data-b="${CSS.escape(selected)}"]`);
    if (!el) return;
    el.classList.add("sb-on");
    const box = el.getBoundingClientRect();
    if (box.bottom < 0 || box.top > (frame.current?.clientHeight ?? 0)) el.scrollIntoView({ block: "center" });
  }, [selected, html, ready, preview]);

  // The frame's document is listened to from here: once, for whatever is drawn in it later.
  useEffect(() => {
    const d = frame.current?.contentDocument;
    if (!ready || !d) return;
    const blockOf = (el: Element | null) => el?.closest<HTMLElement>("[data-b]")?.dataset.b ?? null;
    const down = (e: MouseEvent) => {
      if (preview) return;
      const text = (e.target as Element | null)?.closest<HTMLElement>("[data-e],[data-s]");
      // A text becomes editable before the click lands, so the caret goes where it was clicked.
      if (text && text.contentEditable !== "plaintext-only") text.contentEditable = "plaintext-only";
    };
    const click = (e: MouseEvent) => {
      const target = e.target as Element | null;
      const link = target?.closest<HTMLAnchorElement>("a[href]");
      if (link) {
        e.preventDefault();
        // The menu and the links between pages open the page; while a text of a link is being typed they do not.
        if (link.querySelector("[contenteditable]") || link.isContentEditable) return;
        const go = link.dataset.go ?? store.doc.pages.find((p) => (p.path ? "/" + p.path : "/") === link.getAttribute("href"))?.id;
        if (go && (preview || link.dataset.go)) store.openPage(go);
      }
      if (!preview) store.select(blockOf(target));
    };
    const input = (e: Event) => {
      const el = (e.target as Element | null)?.closest<HTMLElement>("[data-e],[data-s]");
      if (!el) return;
      const text = (el.textContent ?? "").replace(/\s*\n\s*/g, " ");
      if (el.dataset.s === "brand" || el.dataset.s === "foot") store.setSite({ [el.dataset.s]: text }, { typing: "site:" + el.dataset.s, quiet: true });
      else if (el.dataset.e) {
        const id = blockOf(el);
        if (id) store.setIn(id, el.dataset.e, text, { quiet: true });
      }
    };
    const key = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (!el?.isContentEditable) return;
      if (e.key === "Enter" || e.key === "Escape") {
        e.preventDefault();
        el.blur();
      }
    };
    const out = (e: FocusEvent) => {
      const el = e.target as HTMLElement | null;
      if (el?.isContentEditable) el.removeAttribute("contenteditable");
    };
    d.addEventListener("mousedown", down);
    d.addEventListener("click", click);
    d.addEventListener("input", input);
    d.addEventListener("keydown", key);
    d.addEventListener("focusout", out);
    return () => {
      d.removeEventListener("mousedown", down);
      d.removeEventListener("click", click);
      d.removeEventListener("input", input);
      d.removeEventListener("keydown", key);
      d.removeEventListener("focusout", out);
    };
  }, [ready, preview, store]);

  return (
    <div className="sb-canvas" data-narrow={narrow || undefined}>
      {/* The frame shares this page's origin so the editor can reach its document, and runs no script of its own. */}
      <iframe ref={frame} title={t("siteBuilder.preview")} sandbox="allow-same-origin" srcDoc={BLANK} onLoad={() => setReady(true)} />
      <span className="sr-only" aria-live="polite">
        {doc.pages.find((p) => p.id === pageId)?.title}
      </span>
    </div>
  );
}
