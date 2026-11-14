import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { arr, BlockKind, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Prose = BlockData & { title: string; paras: string[] };

/** A few paragraphs of plain text. */
export class ProseBlock extends BlockKind<Prose> {
  readonly type = "prose";
  readonly group = "content" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    { key: "paras", kind: "paras" },
  ];

  empty(lang: Lang) {
    return lang === "ru" ? { title: "Текст", paras: ["Абзац текста."] } : { title: "Text", paras: ["A paragraph of text."] };
  }

  render(b: Prose, c: RenderContext): string {
    return `<div class="${c.cls("prose")}">${arr<string>(b.paras)
      .map((t, i) => `<p${c.e(`paras.${i}`)}>${esc(t)}</p>`)
      .join("")}</div>`;
  }

  styles(s: StyleContext): string[] {
    return [`.${s.cls("prose")}{max-width:64ch}`, `.${s.cls("prose")} p{margin:0 0 1em}`, `.${s.cls("prose")} p:last-child{margin-bottom:0}`];
  }
}
