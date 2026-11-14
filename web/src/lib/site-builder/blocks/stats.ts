import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { arr, BlockKind, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Stat = { value: string; label: string };
export type Stats = BlockData & { title: string; items: Stat[] };

/** A row of numbers with what each counts. The numbers are the admin's: the random site writes none. */
export class StatsBlock extends BlockKind<Stats> {
  readonly type = "stats";
  readonly group = "business" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    {
      key: "items",
      kind: "items",
      of: [
        { key: "value", kind: "text", max: 24 },
        { key: "label", kind: "text", max: 80 },
      ],
    },
  ];

  empty(lang: Lang) {
    const w = lang === "ru" ? ["В цифрах", "что считаем"] : ["In numbers", "what it counts"];
    return { title: w[0]!, items: [12, 3, 48].map((n) => ({ value: String(n), label: w[1]! })) };
  }

  render(b: Stats, c: RenderContext): string {
    const items = arr<Stat>(b.items).map((it, i) => `<div><dd${c.e(`items.${i}.value`)}>${esc(it.value)}</dd><dt${c.e(`items.${i}.label`)}>${esc(it.label)}</dt></div>`);
    return `<dl class="${c.cls("stats")}">${items.join("")}</dl>`;
  }

  styles(s: StyleContext): string[] {
    return [
      `.${s.cls("stats")}{margin:0;display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,160px),1fr));gap:${s.unit}px}`,
      `.${s.cls("stats")} div{display:flex;flex-direction:column;padding-top:${s.unit * 0.75}px;border-top:2px solid var(--ink)}`,
      `.${s.cls("stats")} dd{margin:0;font-family:${s.heading};font-size:2.4em;line-height:1.05;font-weight:700;font-variant-numeric:tabular-nums}`,
      `.${s.cls("stats")} dt{margin-top:6px;color:var(--mu)}`,
    ];
  }
}
