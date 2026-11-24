import { useState } from "react";
import { useSubTemplate, useSubTemplates } from "../../../../api/hooks";
import { QueryBoundary } from "../../../../components/query";
import { Skeleton } from "../../../../components/ui";
import { TemplateList } from "./list";
import type { Core, Saved, View } from "./model";
import { NewTemplate } from "./new-template";
import { SimpleEditor } from "./simple-editor";
import { AppsSummary } from "./summary";
import { NewTextEditor, TextEditor, type TextStart } from "./text-editor";

/** A template being edited: a simple one or a text of a core, new or saved; a text may open
 * with a start of its own (a simple template's YAML). */
type Open = { kind: "simple"; id?: number } | { kind: "text"; core: Core; id?: number; start?: TextStart };

/** The editor of what was saved: a template whose kind a save changed opens in the other one. */
const openOf = (x: Pick<Saved, "id" | "kind" | "core">): Open => (x.kind === "simple" ? { kind: "simple", id: x.id } : { kind: "text", core: x.core, id: x.id });

/**
 * The subscription templates: what each app gets now, every template, and an editor of each
 * kind: a simple template of switches for every app, or the admin's own config of a core
 * with the profile it makes beside it, as it is typed.
 */
export function SubTemplates() {
  const q = useSubTemplates();
  const [open, setOpen] = useState<Open | null>(null);
  const [choosing, setChoosing] = useState(false);
  return (
    <QueryBoundary query={q} pending={<Skeleton style={{ height: 320, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
      {(v) =>
        open ? (
          <Editor v={v} open={open} onClose={() => setOpen(null)} onOpen={setOpen} />
        ) : (
          <div className="flex flex-col gap-4">
            <AppsSummary v={v} />
            <TemplateList v={v} onOpen={(x) => setOpen(openOf(x))} onNew={() => setChoosing(true)} />
            <NewTemplate
              v={v}
              open={choosing}
              onOpenChange={setChoosing}
              onPick={(kind) => {
                setChoosing(false);
                setOpen(kind === "simple" ? { kind: "simple" } : { kind: "text", core: kind });
              }}
            />
          </div>
        )
      }
    </QueryBoundary>
  );
}

/** The editor of open: of a saved template once it is read, of a new one at once. */
function Editor({ v, open, onClose, onOpen }: { v: View; open: Open; onClose: () => void; onOpen: (o: Open) => void }) {
  const one = useSubTemplate(open.id);
  if (open.id === undefined) return <EditorOf v={v} open={open} onClose={onClose} onOpen={onOpen} />;
  return (
    <QueryBoundary query={one} pending={<Skeleton style={{ height: 480, borderRadius: 20 }} />} wrap={(state) => <section className="card glass">{state}</section>}>
      {(saved) => <EditorOf v={v} open={open} saved={saved} onClose={onClose} onOpen={onOpen} />}
    </QueryBoundary>
  );
}

function EditorOf({ v, open, saved, onClose, onOpen }: { v: View; open: Open; saved?: Saved; onClose: () => void; onOpen: (o: Open) => void }) {
  const onSaved = (next: Saved) => onOpen(openOf(next));
  // An editor of another template, or of another kind of it, starts afresh.
  const key = `${open.kind}-${open.id ?? "new"}-${open.kind === "text" && open.start ? "start" : ""}`;
  if (open.kind === "simple") {
    return <SimpleEditor key={key} v={v} saved={saved} onClose={onClose} onSaved={onSaved} onAsText={(start) => onOpen({ kind: "text", core: "mihomo", id: open.id, start })} />;
  }
  if (!saved && !open.start) return <NewTextEditor key={key} v={v} core={open.core} onClose={onClose} onSaved={onSaved} />;
  return <TextEditor key={key} v={v} core={open.core} saved={saved} start={open.start} onClose={onClose} onSaved={onSaved} />;
}
