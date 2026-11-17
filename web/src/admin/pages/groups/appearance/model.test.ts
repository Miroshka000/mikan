import { describe, expect, it } from "vitest";
import { appearanceBody, appearanceDraft, draftValid, ownParts, updateHours, type Appearance } from "./model";

const panelRules = { limit: 1, days: 1, return_hours: 24 };

// A group that leaves everything to the panel, as the API tells it.
const asAll = (): Appearance => ({
  templates: { mihomo: null, singbox: null, xray: null },
  title: null,
  announce: null,
  announce_url: null,
  support_url: null,
  update_hours: null,
  headers: [],
  require_hwid: null,
  unbind_own: false,
  unbind: panelRules,
  page: false,
  addresses: [],
});

describe("appearanceBody", () => {
  it("leaves out what the group leaves to the panel", () => {
    expect(appearanceBody(appearanceDraft(asAll()))).toEqual({ templates: {} });
  });
  it("sends what is the group's own, and an announcement turned off as empty", () => {
    const a: Appearance = {
      ...asAll(),
      templates: { mihomo: 5, singbox: null, xray: null },
      title: "VIP {name}",
      announce: "",
      announce_url: "https://t.me/news",
      update_hours: 6,
      headers: [{ name: "X-Group", value: "vip" }],
      require_hwid: false,
      unbind_own: true,
      unbind: { limit: 3, days: 7, return_hours: 0 },
      addresses: [{ inbound_id: 2, address: "edge.example.com", port: 8443, sni: "", host: "", name: "Edge", cdn: false }],
    };
    const d = appearanceDraft(a);
    expect(d.noAnnounce).toBe(true);
    expect(appearanceBody(d)).toEqual({
      templates: { mihomo: 5 },
      title: "VIP {name}",
      announce: "",
      update_hours: 6,
      headers: [{ name: "X-Group", value: "vip" }],
      require_hwid: false,
      unbind: { limit: 3, days: 7, return_hours: 0 },
      addresses: [{ inbound_id: 2, address: "edge.example.com", port: 8443, sni: "", host: "", name: "Edge", cdn: false }],
    });
  });
  it("trims the texts and drops the headers without a name", () => {
    const d = { ...appearanceDraft(asAll()), title: "  ", supportUrl: " https://t.me/vip ", headers: [{ name: " X-A ", value: "1" }, { name: "", value: "lost" }] };
    expect(appearanceBody(d)).toEqual({ templates: {}, support_url: "https://t.me/vip", headers: [{ name: "X-A", value: "1" }] });
  });
});

describe("updateHours", () => {
  it("takes a whole number of hours from 1 to 168", () => {
    expect(updateHours(" 6 ")).toBe(6);
    expect(updateHours("168")).toBe(168);
    for (const bad of ["0", "169", "1.5", "-2", "six", ""]) expect(updateHours(bad)).toBeNull();
  });
  it("lets the form go with the field empty or right", () => {
    const d = appearanceDraft(asAll());
    expect(draftValid(d)).toBe(true);
    expect(draftValid({ ...d, updateHours: "12" })).toBe(true);
    expect(draftValid({ ...d, updateHours: "0" })).toBe(false);
  });
});

describe("ownParts", () => {
  it("counts the parts of the look that are the group's own", () => {
    expect(ownParts(asAll())).toBe(0);
    expect(ownParts({ ...asAll(), title: "VIP", page: true, unbind_own: true })).toBe(3);
  });
});
