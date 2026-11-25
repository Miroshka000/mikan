import { useEffect, useState } from "react";
import { parseDoc, SiteGenerator } from "../../../lib/site-builder";
import { Canvas } from "./canvas";
import { Inspector } from "./inspector";
import { Library } from "./library";
import { EditorStore } from "./store";
import { TopBar, type View } from "./topbar";

export type Saved = { doc: string; size: number; max: number; seed: string };

/**
 * The editor: the library at the left, the canvas in the middle, the inspector at the right,
 * over the whole window. It opens on the site saved before or, the first time, on a site
 * made for this installation's seed.
 */
export function Editor({ saved }: { saved: Saved }) {
  // One store for as long as the editor is open: saving does not start the editing over.
  const [store] = useState(() => {
    const kept = parseDoc(saved.doc);
    return new EditorStore(kept ?? new SiteGenerator().random(saved.seed, "ru"), saved.seed, kept !== null);
  });
  const [view, setView] = useState<View>({ preview: false, narrow: false });

  // Undo and redo by the keys people expect, when they are not typing in a field.
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (!(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== "z" || el?.closest("input,textarea,[contenteditable]")) return;
      e.preventDefault();
      if (e.shiftKey) store.redo();
      else store.undo();
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [store]);

  return (
    <div className="sb" data-preview={view.preview || undefined}>
      <TopBar store={store} view={view} onView={setView} max={saved.max} />
      <div className="sb-main">
        {view.preview ? null : <Library store={store} />}
        <Canvas store={store} narrow={view.narrow} preview={view.preview} />
        {view.preview ? null : <Inspector store={store} />}
      </div>
    </div>
  );
}
