import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { arr, BlockKind, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Split = BlockData & { title: string; paras: string[]; image: string; side: "left" | "right" };

/** A picture with text beside it. Without a picture it is the text alone. */
export class SplitBlock extends BlockKind<Split> {
  readonly type = "split";
  readonly group = "media" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    { key: "paras", kind: "paras" },
    { key: "image", kind: "image" },
    { key: "side", kind: "choice", options: ["left", "right"] },
  ];

  empty(lang: Lang) {
    return lang === "ru" ? { title: "Картинка и текст", paras: ["Текст рядом с картинкой."], image: "", side: "right" as const } : { title: "A picture and text", paras: ["Text beside the picture."], image: "", side: "right" as const };
  }

  render(b: Split, c: RenderContext): string {
    const img = c.image(b.image);
    const text = `<div class="${c.cls("prose")}">${arr<string>(b.paras)
      .map((t, i) => `<p${c.e(`paras.${i}`)}>${esc(t)}</p>`)
      .join("")}</div>`;
    if (!img) return text;
    const pic = `<img class="${c.cls("pic")}" src="${esc(img.src)}" width="${img.w}" height="${img.h}" alt="" loading="lazy">`;
    return `<div class="${c.cls("pair")}">${b.side === "left" ? pic + text : text + pic}</div>`;
  }

  styles(s: StyleContext): string[] {
    return [`.${s.cls("pair")}{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:${s.unit * 2}px;align-items:center}`, `@media (max-width:720px){.${s.cls("pair")}{grid-template-columns:1fr}}`];
  }
}
