import { describe, expect, it } from "vitest";
import { lineOf, newRule, rowOf, rowsOf, takesNoResolve, textOf, type RuleRow } from "./rule-model";

const strip = (rows: RuleRow[]) => rows.map(({ key: _, ...r }) => r);

describe("rowsOf", () => {
  it("makes a row of each line, the comments rows of their own, and drops the blank lines", () => {
    const { rows, lines } = rowsOf("DOMAIN-SUFFIX, example.com ,DIRECT\n\n# banks\n  ip-cidr,10.0.0.0/8,PROXY,No-Resolve  \n");
    expect(strip(rows)).toEqual([
      { kind: "rule", type: "DOMAIN-SUFFIX", value: "example.com", target: "DIRECT", noResolve: false },
      { kind: "comment", text: "# banks" },
      { kind: "rule", type: "IP-CIDR", value: "10.0.0.0/8", target: "PROXY", noResolve: true },
    ]);
    expect(lines).toEqual([1, 3, 4]);
  });
  it("keeps a line that is no rule it can show as it is written", () => {
    for (const line of ["DOMAIN,a.example", "MATCH,a,b,c,d", "AND,((NETWORK,UDP),(DST-PORT,443)),REJECT", "WHAT,a.example,DIRECT", "DOMAIN,a.example,DIRECT,no-resolve", "GEOIP,ru,DIRECT,maybe"]) {
      expect(strip([rowOf(line)])).toEqual([{ kind: "raw", text: line }]);
    }
  });
  it("gives every row a key of its own", () => {
    const { rows } = rowsOf("DOMAIN,a,DIRECT\nDOMAIN,a,DIRECT");
    expect(new Set(rows.map((r) => r.key)).size).toBe(2);
  });
});

describe("textOf", () => {
  it("gives the text back line for line, comments where they were", () => {
    const text = "DOMAIN-SUFFIX,example.com,DIRECT\n# banks\nIP-CIDR,10.0.0.0/8,PROXY,no-resolve\nAND,((NETWORK,UDP),(DST-PORT,443)),REJECT";
    expect(textOf(rowsOf(text).rows)).toBe(text);
  });
  it("writes the rules as the server reads them", () => {
    expect(lineOf({ key: "k", kind: "rule", type: "DOMAIN", value: " a.example ", target: "PROXY", noResolve: true })).toBe("DOMAIN,a.example,PROXY");
    expect(lineOf({ key: "k", kind: "rule", type: "GEOIP", value: "ru", target: "DIRECT", noResolve: true })).toBe("GEOIP,ru,DIRECT,no-resolve");
    expect(lineOf({ key: "k", kind: "comment", text: "banks" })).toBe("# banks");
  });
  it("puts the rows in the order they are dragged to", () => {
    const { rows } = rowsOf("DOMAIN,a,DIRECT\n# b\nDOMAIN,c,PROXY");
    expect(textOf([rows[2]!, rows[0]!, rows[1]!])).toBe("DOMAIN,c,PROXY\nDOMAIN,a,DIRECT\n# b");
  });
});

describe("newRule", () => {
  it("is a site of no value yet to the target given", () => {
    expect(newRule("DIRECT")).toMatchObject({ kind: "rule", type: "DOMAIN-SUFFIX", value: "", target: "DIRECT", noResolve: false });
  });
  it("only an IP type takes no-resolve", () => {
    expect(takesNoResolve("IP-CIDR")).toBe(true);
    expect(takesNoResolve("DOMAIN")).toBe(false);
  });
});
