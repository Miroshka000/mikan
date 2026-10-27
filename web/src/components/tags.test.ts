import { describe, expect, it } from "vitest";
import { hasTag, parseTags } from "./tags";

describe("parseTags", () => {
  it("splits by commas, trims, drops the empty ones and repeats in any case", () => {
    expect(parseTags(" germany , whitelist,, Germany ,white list ")).toEqual(["germany", "whitelist", "white list"]);
    expect(parseTags("  ")).toEqual([]);
  });
});

describe("hasTag", () => {
  it("matches in any letter case, as the API's filter", () => {
    expect(hasTag(["Whitelist"], "whitelist")).toBe(true);
    expect(hasTag(["whitelist"], "white")).toBe(false);
  });
});
