import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { arr, BlockKind, str, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Quote = { text: string; who: string };
export type Quotes = BlockData & { title: string; items: Quote[] };

/** What people said, with who said it. The words are the admin's: the random site writes none. */
export class QuotesBlock extends BlockKind<Quotes> {
  readonly type = "quotes";
  readonly group = "business" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    {
      key: "items",
      kind: "items",
      of: [
        { key: "text", kind: "area", max: 500 },
        { key: "who", kind: "text", max: 80 },
      ],
    },
  ];

  empty(lang: Lang) {
    const w = lang === "ru" ? ["Отзывы", "Слова человека.", "Имя"] : ["What people say", "A person's words.", "Name"];
    return { title: w[0]!, items: [1, 2].map(() => ({ text: w[1]!, who: w[2]! })) };
  }

  render(b: Quotes, c: RenderContext): string {
    const items = arr<Quote>(b.items).map((q, i) => `<figure><blockquote${c.e(`items.${i}.text`)}>${esc(q.text)}</blockquote>${str(q.who) ? `<figcaption${c.e(`items.${i}.who`)}>${esc(q.who)}</figcaption>` : ""}</figure>`);
    return `<div class="${c.cls("quotes")}">${items.join("")}</div>`;
  }

  styles(s: StyleContext): string[] {
    return [
      `.${s.cls("quotes")}{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,280px),1fr));gap:${s.unit * 1.5}px}`,
      `.${s.cls("quotes")} figure{margin:0;padding-left:${s.unit}px;border-left:3px solid var(--ac)}`,
      `.${s.cls("quotes")} blockquote{margin:0;font-family:${s.heading};font-size:1.15em;line-height:1.45}`,
      `.${s.cls("quotes")} figcaption{margin-top:10px;color:var(--mu);font-size:.9em}`,
    ];
  }
}
