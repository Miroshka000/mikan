import { describe, expect, it } from "vitest";
import { GiB } from "../../../lib/format";
import { batchName, batchRange, csvOf, draftDiffers, draftOf, emptyDraft, hasNumber, linksText, specOf, templateBody, type Template } from "./model";

const template = (over: Partial<Template> = {}): Template => ({
  id: 7,
  name: "Class 7",
  tariff_id: 2,
  traffic_limit: 50 * GiB,
  device_limit: 0,
  speed_limit: null,
  term_days: 30,
  starts_on_connect: true,
  folder_id: 3,
  group_id: null,
  tags: ["school", "7a"],
  note: "class 7",
  name_pattern: "pupil-{nn}",
  updated_at: "2026-11-24T00:00:00Z",
  ...over,
});

describe("batch names", () => {
  it("puts the number in place of every mark, with zeros up to the mark's length", () => {
    expect(batchName("client-{n}", 7)).toBe("client-7");
    expect(batchName("school-{nnn}", 9)).toBe("school-009");
    expect(batchName("{nn} · {n}", 5)).toBe("05 · 5");
    expect(batchName("{nn}", 100)).toBe("100");
  });
  it("knows a pattern without a mark", () => {
    expect(hasNumber("client")).toBe(false);
    expect(hasNumber("client-{x}")).toBe(false);
    expect(hasNumber("client-{n}")).toBe(true);
  });
  it("names the first and the last client of a batch", () => {
    expect(batchRange("pupil-{nn}", 9, 3)).toEqual({ first: "pupil-09", last: "pupil-11" });
    expect(batchRange("pupil-{nn}", 9, 1)).toEqual({ first: "pupil-09", last: "pupil-09" });
  });
});

describe("the form and a template", () => {
  it("reads a template's limits: null is the tariff's, 0 is none", () => {
    const d = draftOf(template());
    expect(d.traffic).toBe(50 * GiB);
    expect(d.devices).toBeNull();
    expect(d.speed).toBeUndefined();
    expect(d.term).toBe(30);
    expect(d.wait).toBe(true);
    expect(d.folderId).toBe(3);
    expect(d.groupId).toBeNull();
    expect(d.tags).toBe("school, 7a");
    expect(d.pattern).toBe("pupil-{nn}");
  });
  it("sends what goes over the tariff and leaves the rest out", () => {
    expect(specOf(draftOf(template()))).toEqual({
      tariff_id: 2,
      traffic_limit: 50 * GiB,
      device_limit: 0,
      term_days: 30,
      starts_on_connect: true,
      folder_id: 3,
      tags: ["school", "7a"],
      note: "class 7",
    });
    expect(specOf(emptyDraft(4))).toEqual({ tariff_id: 4, tags: [] });
  });
  it("round-trips a template through the form", () => {
    const t = template();
    expect(templateBody(draftOf(t), t.name)).toEqual({ name: "Class 7", name_pattern: "pupil-{nn}", ...specOf(draftOf(t)) });
    expect(draftDiffers(draftOf(t), t)).toBe(false);
    expect(draftDiffers({ ...draftOf(t), devices: 2 }, t)).toBe(true);
    expect(draftDiffers({ ...draftOf(t), tags: "7a, school" }, t)).toBe(true);
  });
});

describe("what a batch hands out", () => {
  const users = [
    { id: 1, name: "pupil-01", sub_url: "https://vpn.example.com/sub/a" },
    { id: 2, name: 'Иван "Ваня", 7a', sub_url: "https://vpn.example.com/sub/b" },
  ];
  it("lists every client with the link", () => {
    expect(linksText(users)).toBe("pupil-01 — https://vpn.example.com/sub/a\nИван \"Ваня\", 7a — https://vpn.example.com/sub/b");
  });
  it("writes a CSV with the names quoted where they need it", () => {
    expect(csvOf(users)).toBe('name,link\r\npupil-01,https://vpn.example.com/sub/a\r\n"Иван ""Ваня"", 7a",https://vpn.example.com/sub/b\r\n');
  });
  it("keeps a name a spreadsheet would run as a formula as text", () => {
    expect(csvOf([{ id: 3, name: "=HYPERLINK(1)", sub_url: "https://vpn.example.com/sub/c" }])).toBe("name,link\r\n'=HYPERLINK(1),https://vpn.example.com/sub/c\r\n");
    expect(csvOf([{ id: 4, name: "-1,2", sub_url: "" }])).toBe("name,link\r\n\"'-1,2\",\r\n");
  });
});
