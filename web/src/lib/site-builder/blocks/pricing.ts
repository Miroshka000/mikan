import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { arr, BlockKind, str, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Plan = { name: string; price: string; note: string; points: string[] };
export type Pricing = BlockData & { title: string; plans: Plan[] };

/** Plans side by side: a name, a price, and what the price holds. */
export class PricingBlock extends BlockKind<Pricing> {
  readonly type = "pricing";
  readonly group = "business" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    {
      key: "plans",
      kind: "items",
      of: [
        { key: "name", kind: "text", max: 60 },
        { key: "price", kind: "text", max: 40 },
        { key: "note", kind: "text", max: 120 },
        { key: "points", kind: "lines" },
      ],
    },
  ];

  empty(lang: Lang) {
    const w = lang === "ru" ? ["Цены", "Тариф", "за месяц", "Что входит"] : ["Prices", "Plan", "a month", "What it holds"];
    return { title: w[0]!, plans: [1, 2].map((n) => ({ name: `${w[1]} ${n}`, price: `${n * 10}`, note: w[2]!, points: [w[3]!] })) };
  }

  render(b: Pricing, c: RenderContext): string {
    const plans = arr<Plan>(b.plans).map((p, i) => {
      const points = arr<string>(p.points).filter((x) => x.trim());
      return (
        `<li><h3${c.e(`plans.${i}.name`)}>${esc(p.name)}</h3><p class="${c.cls("price")}"${c.e(`plans.${i}.price`)}>${esc(p.price)}</p>` +
        (str(p.note) ? `<p class="${c.cls("meta")}"${c.e(`plans.${i}.note`)}>${esc(p.note)}</p>` : "") +
        (points.length ? `<ul>${points.map((x) => `<li>${esc(x)}</li>`).join("")}</ul>` : "") +
        `</li>`
      );
    });
    return `<ul class="${c.cls("plans")}">${plans.join("")}</ul>`;
  }

  styles(s: StyleContext): string[] {
    return [
      `.${s.cls("plans")}{list-style:none;margin:0;padding:0;display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,230px),1fr));gap:${s.unit}px}`,
      `.${s.cls("plans")}>li{padding:${s.unit * 1.25}px;border:1px solid var(--ln);border-radius:${s.radius}px}`,
      `.${s.cls("price")}{margin:8px 0 2px;font-family:${s.heading};font-size:2em;line-height:1.1;font-weight:700;font-variant-numeric:tabular-nums}`,
      `.${s.cls("plans")} ul{margin:${s.unit * 0.75}px 0 0;padding:${s.unit * 0.75}px 0 0 1.1em;border-top:1px solid var(--ln);color:var(--mu)}`,
      `.${s.cls("plans")} ul li{margin:4px 0}`,
    ];
  }
}
