import { esc } from "../html";
import type { BlockData, Lang } from "../model";
import { arr, BlockKind, type FieldSpec, type RenderContext, type StyleContext } from "./block";

export type Code = BlockData & { title: string; lines: string[] };

/** Lines of a terminal: one that starts with "$ " is a command, the rest is what it printed. */
export class CodeBlock extends BlockKind<Code> {
  readonly type = "code";
  readonly group = "content" as const;
  readonly fields: FieldSpec[] = [
    { key: "title", kind: "text", max: 140 },
    { key: "lines", kind: "lines", mono: true },
  ];

  empty(lang: Lang) {
    return { title: lang === "ru" ? "Как запустить" : "How to run it", lines: ["$ echo hello", "hello"] };
  }

  render(b: Code, c: RenderContext): string {
    const lines = arr<string>(b.lines).map((l) => (l.startsWith("$ ") ? `<span>$</span> ${esc(l.slice(2))}` : esc(l)));
    return `<pre class="${c.cls("code")}"><code>${lines.join("\n")}</code></pre>`;
  }

  styles(s: StyleContext): string[] {
    return [
      `.${s.cls("code")}{margin:0;padding:${s.unit}px;overflow-x:auto;background:var(--sf);border:1px solid var(--ln);border-radius:${s.radius}px;font:14px/1.7 ${s.mono};tab-size:2}`,
      `.${s.cls("code")} span{color:var(--mu);user-select:none}`,
    ];
  }
}
