/**
 * The admin's own rules as rows: a rule is a type, a value, a target and no-resolve, a line of
 * the text a row. The text is what is saved; the rows are made of it and give it back line for
 * line, so the text view and the rows view can take turns. A comment ("#") stays a row of its
 * own, a line the rows cannot show as a rule stays as it is written, blank lines go.
 */

/** The types the panel takes (routing.ParseRules), the most used first. */
export const RULE_TYPES = [
  "DOMAIN-SUFFIX",
  "DOMAIN",
  "DOMAIN-KEYWORD",
  "GEOSITE",
  "GEOIP",
  "IP-CIDR",
  "IP-CIDR6",
  "PROCESS-NAME",
  "PROCESS-PATH",
  "DST-PORT",
  "SRC-PORT",
  "NETWORK",
  "SRC-IP-CIDR",
  "IP-SUFFIX",
  "IP-ASN",
  "SRC-GEOIP",
  "DOMAIN-WILDCARD",
  "DOMAIN-REGEX",
] as const;

/** The types that resolve a name to match unless told no-resolve (routing's noResolve). */
const RESOLVING = new Set(["GEOIP", "IP-CIDR", "IP-CIDR6", "IP-SUFFIX", "IP-ASN"]);

/** Whether a rule of type takes no-resolve. */
export const takesNoResolve = (type: string) => RESOLVING.has(type);

type Base = { key: string };
export type RuleLine = Base & { kind: "rule"; type: string; value: string; target: string; noResolve: boolean };
type CommentLine = Base & { kind: "comment"; text: string };
type RawLine = Base & { kind: "raw"; text: string };
export type RuleRow = RuleLine | CommentLine | RawLine;

let last = 0;
/** A key no other row of the page has: the rows are put in order by it. */
const rowKey = () => `rule-${++last}`;

/** The rule a line is, or null for a line that is no rule of a known type with three or four fields. */
function ruleOf(line: string): Omit<RuleLine, "key"> | null {
  const f = line.split(",").map((x) => x.trim());
  if (f.length < 3 || f.length > 4) return null;
  const type = f[0]!.toUpperCase();
  if (!(RULE_TYPES as readonly string[]).includes(type)) return null;
  if (f.length === 4 && (f[3]!.toLowerCase() !== "no-resolve" || !takesNoResolve(type))) return null;
  return { kind: "rule", type, value: f[1]!, target: f[2]!, noResolve: f.length === 4 };
}

/** The row of one line of the text that is not blank. */
export function rowOf(line: string): RuleRow {
  const text = line.trim();
  if (text.startsWith("#")) return { key: rowKey(), kind: "comment", text };
  const rule = ruleOf(text);
  return rule ? { key: rowKey(), ...rule } : { key: rowKey(), kind: "raw", text };
}

/** The rows of the text, and for each the line it is on (from 1): what a refusal names. */
export function rowsOf(text: string): { rows: RuleRow[]; lines: number[] } {
  const rows: RuleRow[] = [];
  const lines: number[] = [];
  text.split("\n").forEach((raw, i) => {
    if (!raw.trim()) return;
    rows.push(rowOf(raw));
    lines.push(i + 1);
  });
  return { rows, lines };
}

/** The line of a row. A comment keeps its "#": a row that lost it is still a comment. */
export function lineOf(row: RuleRow): string {
  switch (row.kind) {
    case "rule":
      return `${row.type},${row.value.trim()},${row.target}${row.noResolve && takesNoResolve(row.type) ? ",no-resolve" : ""}`;
    case "comment":
      return row.text.trim().startsWith("#") ? row.text.trim() : `# ${row.text.trim()}`;
    case "raw":
      return row.text.trim();
  }
}

/** The text of the rows, a line each. */
export const textOf = (rows: RuleRow[]) => rows.map(lineOf).join("\n");

/** A new rule of the rows: a site sent past the tunnel, the commonest. */
export const newRule = (target: string): RuleLine => ({ key: rowKey(), kind: "rule", type: "DOMAIN-SUFFIX", value: "", target, noResolve: false });

/** A new comment of the rows, ready for its words after the "#". */
export const newComment = (): RuleRow => ({ key: rowKey(), kind: "comment", text: "# " });
