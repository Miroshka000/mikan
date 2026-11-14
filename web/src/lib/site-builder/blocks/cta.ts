import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { BlockKind, str, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Cta = BlockData & { title: string; text: string; label: string; page: string };

/** A closing line with a link to another page of the site. */
export class CtaBlock extends BlockKind<Cta> {
  readonly type = "cta";
  readonly group = "structure" as const;
  override readonly titled = false;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    { key: "text", kind: "area", max: 400 },
    { key: "label", kind: "text", max: 40 },
    { key: "page", kind: "page" },
  ];

  empty(lang: Lang) {
    return lang === "ru" ? { title: "Что дальше", text: "Одна фраза о следующем шаге.", label: "Подробнее", page: "" } : { title: "What next", text: "One line on the next step.", label: "Read more", page: "" };
  }

  render(b: Cta, c: RenderContext): string {
    const link = str(b.label).trim() ? `<a class="${c.cls("more")}" href="${esc(c.href(str(b.page)))}"${c.e("label")}>${esc(b.label)}</a>` : "";
    return `<div class="${c.cls("cta")}"><div><h2${c.e("title")}>${esc(b.title)}</h2>${str(b.text) ? `<p${c.e("text")}>${esc(b.text)}</p>` : ""}</div>${link}</div>`;
  }

  styles(s: StyleContext): string[] {
    return [
      `.${s.cls("cta")}{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:${s.unit}px ${s.unit * 2}px;padding:${s.unit * 1.5}px;border-radius:${s.radius}px;background:var(--sf)}`,
      `.${s.cls("cta")} h2{margin:0 0 6px}`,
      `.${s.cls("cta")} p{margin:0;color:var(--mu);max-width:56ch}`,
    ];
  }
}
