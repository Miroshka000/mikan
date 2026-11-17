import { esc } from "../html";
import type { BlockData, Lang, ListStyle } from "../model";
import { arr, BlockKind, str, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type ListItem = { title: string; text: string; meta?: string };
export type List = BlockData & { title: string; style: ListStyle; items: ListItem[] };

/** A list of things with a name and a few words each: as cards, as rows with a note beside, or as numbered steps. */
export class ListBlock extends BlockKind<List> {
  readonly type = "list";
  readonly group = "content" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    { key: "style", kind: "choice", options: ["cards", "rows", "steps"] },
    {
      key: "items",
      kind: "items",
      of: [
        { key: "title", kind: "text", max: 140 },
        { key: "meta", kind: "text", max: 60 },
        { key: "text", kind: "area", max: 600 },
      ],
    },
  ];

  empty(lang: Lang) {
    const w = lang === "ru" ? ["Список", "Пункт", "Несколько слов о нём."] : ["A list", "An item", "A few words about it."];
    return { title: w[0]!, style: "cards" as ListStyle, items: [1, 2, 3].map((n) => ({ title: `${w[1]} ${n}`, text: w[2]! })) };
  }

  render(b: List, c: RenderContext): string {
    const items = arr<ListItem>(b.items);
    const meta = (it: ListItem, i: number) => (str(it.meta) ? `<span class="${c.cls("meta")}"${c.e(`items.${i}.meta`)}>${esc(it.meta!)}</span>` : "");
    const head = (it: ListItem, i: number) => `<h3${c.e(`items.${i}.title`)}>${esc(it.title)}</h3>`;
    const text = (it: ListItem, i: number) => `<p${c.e(`items.${i}.text`)}>${esc(it.text)}</p>`;
    if (b.style === "steps") return `<ol class="${c.cls("steps")}">${items.map((it, i) => `<li>${head(it, i)}${text(it, i)}</li>`).join("")}</ol>`;
    if (b.style === "rows") return `<ul class="${c.cls("rows")}">${items.map((it, i) => `<li>${meta(it, i)}<div>${head(it, i)}${text(it, i)}</div></li>`).join("")}</ul>`;
    return `<ul class="${c.cls("cards")}">${items.map((it, i) => `<li>${head(it, i)}${meta(it, i)}${text(it, i)}</li>`).join("")}</ul>`;
  }

  styles(s: StyleContext): string[] {
    const filled = s.rng.chance(0.5);
    return [
      `.${s.cls("cards")}{list-style:none;margin:0;padding:0;display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,${s.rng.pick([230, 250, 270])}px),1fr));gap:${s.unit}px}`,
      `.${s.cls("cards")} li{padding:${s.unit}px;border-radius:${s.radius}px;${filled ? "background:var(--sf)" : "border:1px solid var(--ln)"}}`,
      `.${s.cls("cards")} p,.${s.cls("rows")} p,.${s.cls("steps")} p{margin:0;color:var(--mu)}`,
      `.${s.cls("meta")}{display:block;font-size:.8em;color:var(--mu);font-variant-numeric:tabular-nums;margin-bottom:8px}`,
      `.${s.cls("rows")}{list-style:none;margin:0;padding:0}`,
      `.${s.cls("rows")} li{display:grid;grid-template-columns:${s.rng.pick([150, 160, 170])}px minmax(0,1fr);gap:${s.unit}px;padding-block:${s.unit}px;border-top:1px solid var(--ln)}`,
      `.${s.cls("rows")} li:first-child{border-top:0;padding-top:0}`,
      `.${s.cls("rows")} .${s.cls("meta")}{margin:3px 0 0}`,
      `.${s.cls("steps")}{margin:0;padding:0;list-style:none;counter-reset:${s.cls("n")};display:grid;gap:${s.unit}px;max-width:68ch}`,
      `.${s.cls("steps")} li{counter-increment:${s.cls("n")};display:grid;grid-template-columns:40px minmax(0,1fr);column-gap:${s.unit / 2}px}`,
      `.${s.cls("steps")} li::before{content:counter(${s.cls("n")});grid-row:span 2;width:32px;height:32px;display:grid;place-items:center;border-radius:${Math.min(s.radius, 16)}px;background:var(--ac);color:var(--on);font-weight:700;font-size:15px;font-variant-numeric:tabular-nums}`,
      `@media (max-width:720px){.${s.cls("rows")} li{grid-template-columns:1fr;gap:4px}}`,
    ];
  }
}
