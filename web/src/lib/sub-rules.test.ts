import { describe, expect, it } from "vitest";
import type { Schemas } from "../api/client";
import { t } from "../i18n";
import { cleanRule, ruleLabel } from "./sub-rules";

const rule = (action: Schemas["Action"]): Schemas["Rule"] => ({ name: "  r  ", enabled: true, when: { op: "and", conds: [], groups: [] }, do: action });

describe("cleanRule", () => {
  it("keeps only what the answer uses", () => {
    const r = cleanRule(rule({ respond: "403", format: "clash", text: "x", html: "<p>", skip_hwid: true, template: 3, headers: [{ name: "X-A", value: "1" }] }));
    expect(r.do).toEqual({ respond: "403", headers: [{ name: "X-A", value: "1" }] });
    expect(r.name).toBe("r");
  });
  it("keeps the profile's own options on serve and the page on html", () => {
    expect(cleanRule(rule({ respond: "serve", format: "uri", skip_hwid: true, html: "<p>" })).do).toEqual({ respond: "serve", format: "uri", skip_hwid: true });
    expect(cleanRule(rule({ respond: "html", html: "<h1>Happ</h1>", text: "t" })).do).toEqual({ respond: "html", html: "<h1>Happ</h1>" });
  });
  it("sends no headers with a dropped connection, nor an empty list", () => {
    expect(cleanRule(rule({ respond: "drop", headers: [{ name: "X-A", value: "1" }] })).do).toEqual({ respond: "drop" });
    expect(cleanRule(rule({ respond: "stub", text: "  ", headers: [] })).do).toEqual({ respond: "stub" });
  });
});

describe("ruleLabel", () => {
  it("words the panel's own decisions and leaves a rule's name as it is", () => {
    expect(ruleLabel("builtin:ssr")).toBe(t("settings.subRules.rule.ssr"));
    expect(ruleLabel("builtin:old")).toBe(t("settings.subRules.rule.old"));
    expect(ruleLabel("apps")).toBe(t("settings.subRules.rule.apps"));
    expect(ruleLabel("")).toBe(t("settings.subRules.rule.none"));
    expect(ruleLabel("no curl")).toBe("no curl");
  });
});
