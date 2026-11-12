import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { arr, BlockKind, str, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type TableRow = { name: string; note: string; value: string };
export type Table = BlockData & { title: string; rows: TableRow[] };

/** Rows of a name, a note under it and a value at the right: prices, a schedule, a list of versions. */
export class TableBlock extends BlockKind<Table> {
  readonly type = "table";
  readonly group = "content" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    {
      key: "rows",
      kind: "items",
      of: [
        { key: "name", kind: "text", max: 120 },
        { key: "value", kind: "text", max: 40 },
        { key: "note", kind: "text", max: 160 },
      ],
    },
  ];

  empty(lang: Lang) {
    const w = lang === "ru" ? ["Таблица", "Название", "пояснение", "значение"] : ["A table", "Name", "a note", "value"];
    return { title: w[0]!, rows: [1, 2].map(() => ({ name: w[1]!, note: w[2]!, value: w[3]! })) };
  }

  render(b: Table, c: RenderContext): string {
    const rows = arr<TableRow>(b.rows).map((r, i) => {
      const note = str(r.note) ? `<span${c.e(`rows.${i}.note`)}>${esc(r.note)}</span>` : "";
      return `<tr><th scope="row"><b${c.e(`rows.${i}.name`)}>${esc(r.name)}</b>${note}</th><td${c.e(`rows.${i}.value`)}>${esc(r.value)}</td></tr>`;
    });
    return `<table class="${c.cls("table")}"><tbody>${rows.join("")}</tbody></table>`;
  }

  styles(s: StyleContext): string[] {
    return [
      `.${s.cls("table")}{width:100%;border-collapse:collapse;max-width:820px}`,
      `.${s.cls("table")} th,.${s.cls("table")} td{padding:${Math.round(s.unit * 0.6)}px 0;border-top:1px solid var(--ln);vertical-align:baseline}`,
      `.${s.cls("table")} tr:first-child th,.${s.cls("table")} tr:first-child td{border-top:0}`,
      `.${s.cls("table")} th{text-align:left;font-weight:400;padding-right:${s.unit}px}`,
      `.${s.cls("table")} th b{font-weight:600}`,
      `.${s.cls("table")} th span{display:block;color:var(--mu);font-size:.92em}`,
      `.${s.cls("table")} td{text-align:right;white-space:nowrap;font-variant-numeric:tabular-nums}`,
      `@media (max-width:720px){.${s.cls("table")} td{white-space:normal}}`,
    ];
  }
}
