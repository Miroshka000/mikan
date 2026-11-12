import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { arr, BlockKind, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Question = { q: string; a: string };
export type Faq = BlockData & { title: string; items: Question[] };

/** Questions that open to their answers: the browser's own details element, no script. */
export class FaqBlock extends BlockKind<Faq> {
  readonly type = "faq";
  readonly group = "business" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    {
      key: "items",
      kind: "items",
      of: [
        { key: "q", kind: "text", max: 200 },
        { key: "a", kind: "area", max: 800 },
      ],
    },
  ];

  empty(lang: Lang) {
    const w = lang === "ru" ? ["Вопросы и ответы", "Вопрос", "Ответ на него."] : ["Questions and answers", "A question", "The answer to it."];
    return { title: w[0]!, items: [1, 2].map(() => ({ q: w[1]!, a: w[2]! })) };
  }

  render(b: Faq, c: RenderContext): string {
    const items = arr<Question>(b.items).map((it, i) => `<details${i === 0 ? " open" : ""}><summary${c.e(`items.${i}.q`)}>${esc(it.q)}</summary><p${c.e(`items.${i}.a`)}>${esc(it.a)}</p></details>`);
    return `<div class="${c.cls("faq")}">${items.join("")}</div>`;
  }

  styles(s: StyleContext): string[] {
    return [
      `.${s.cls("faq")}{max-width:72ch}`,
      `.${s.cls("faq")} details{border-top:1px solid var(--ln);padding:${Math.round(s.unit * 0.7)}px 0}`,
      `.${s.cls("faq")} details:last-child{border-bottom:1px solid var(--ln)}`,
      `.${s.cls("faq")} summary{cursor:pointer;font-weight:600;list-style-position:outside;padding:6px 0}`,
      `.${s.cls("faq")} summary:focus-visible{outline:2px solid var(--ac);outline-offset:3px}`,
      `.${s.cls("faq")} p{margin:8px 0 4px;color:var(--mu)}`,
    ];
  }
}
