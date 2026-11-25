import { describe, expect, it } from "vitest";
import { groupsPicking, hasRoom, servedOf, type Head, type View } from "./model";

const head = (id: number, core: Head["core"], kind: Head["kind"], def = false) => ({ id, core, kind, name: `t${id}`, default: def, size: 0, updated_at: "", rules: [] }) as Head;

describe("servedOf", () => {
  it("is the core's own default first", () => {
    const ts = [head(1, "mihomo", "simple", true), head(2, "singbox", "text", true)];
    expect(servedOf(ts, "singbox")).toEqual({ by: "own", template: ts[1] });
    expect(servedOf(ts, "mihomo")).toEqual({ by: "own", template: ts[0] });
  });
  it("is the Clash default for sing-box and Xray with none of their own", () => {
    const ts = [head(1, "mihomo", "simple", true), head(3, "xray", "text")];
    expect(servedOf(ts, "xray")).toEqual({ by: "clash", template: ts[0] });
  });
  it("is the built-in profile with no default to take", () => {
    const ts = [head(1, "mihomo", "text"), head(2, "singbox", "text")];
    expect(servedOf(ts, "mihomo")).toEqual({ by: "builtin" });
    expect(servedOf(ts, "singbox")).toEqual({ by: "builtin" });
  });
});

describe("groupsPicking", () => {
  it("names the groups of each template, of any core", () => {
    const groups = [
      { name: "VIP", appearance: { templates: { mihomo: 1, singbox: 2, xray: null } } },
      { name: "Trial", appearance: { templates: { mihomo: 1, singbox: null, xray: null } } },
      { name: "Plain", appearance: { templates: { mihomo: null, singbox: null, xray: null } } },
    ];
    const by = groupsPicking(groups);
    expect(by.get(1)).toEqual(["VIP", "Trial"]);
    expect(by.get(2)).toEqual(["VIP"]);
    expect(by.has(3)).toBe(false);
  });
});

describe("hasRoom", () => {
  it("counts a core's templates, the simple ones as Clash's", () => {
    const v = { templates: [head(1, "mihomo", "simple"), head(2, "mihomo", "text")], max: 2, max_size: 1 } as View;
    expect(hasRoom(v, "mihomo")).toBe(false);
    expect(hasRoom(v, "xray")).toBe(true);
  });
});
