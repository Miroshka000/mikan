import { describe, expect, it } from "vitest";
import { boardColumns, boxAt, matching, moveService } from "./board-model";

const services = [
  { id: "ads", icon: "🚫", name: "Ads" },
  { id: "youtube", icon: "📺", name: "YouTube" },
  { id: "telegram", icon: "✈️", name: "Telegram" },
];
const targets = [
  { id: "", label: "As the mode" },
  { id: "vpn", label: "VPN" },
  { id: "direct", label: "Direct" },
  { id: "node:2", label: "Server 2" },
];
const missing = (id: string) => `gone ${id}`;

describe("boardColumns", () => {
  it("puts each service in the column of its target, the rest in the mode's, in the catalog's order", () => {
    const cols = boardColumns(services, { telegram: "vpn", ads: "vpn", youtube: "node:2" }, targets, missing);
    expect(cols.map((c) => [c.target, c.items.map((s) => s.id)])).toEqual([
      ["", []],
      ["vpn", ["ads", "telegram"]],
      ["direct", []],
      ["node:2", ["youtube"]],
    ]);
  });
  it("follows the mode for every service with none sent", () => {
    expect(boardColumns(services, undefined, targets, missing)[0]!.items).toHaveLength(3);
  });
  it("gives a target it does not know a column of its own, once", () => {
    const cols = boardColumns(services, { ads: "node:9", youtube: "node:9" }, targets, missing);
    expect(cols).toHaveLength(5);
    expect(cols[4]).toMatchObject({ target: "node:9", label: "gone node:9" });
    expect(cols[4]!.items.map((s) => s.id)).toEqual(["ads", "youtube"]);
  });
});

describe("moveService", () => {
  it("sends a service to a target and back to the mode, leaving the others", () => {
    const sent = { ads: "block" };
    const moved = moveService(sent, "youtube", "direct");
    expect(moved).toEqual({ ads: "block", youtube: "direct" });
    expect(moveService(moved, "youtube", "")).toEqual({ ads: "block" });
    expect(sent).toEqual({ ads: "block" });
    expect(moveService(undefined, "ads", "vpn")).toEqual({ ads: "vpn" });
  });
});

describe("boxAt", () => {
  const boxes = [{ left: 0, right: 100, top: 0, bottom: 50 }, undefined, { left: 0, right: 100, top: 60, bottom: 200 }];
  it("finds the box under the point, none between them", () => {
    expect(boxAt(50, 10, boxes)).toBe(0);
    expect(boxAt(50, 100, boxes)).toBe(2);
    expect(boxAt(50, 55, boxes)).toBe(-1);
    expect(boxAt(150, 10, boxes)).toBe(-1);
  });
});

describe("matching", () => {
  it("finds by the name in any case and by the id", () => {
    const name = (s: (typeof services)[number]) => s.name;
    expect(matching(services, " tube ", name).map((s) => s.id)).toEqual(["youtube"]);
    expect(matching(services, "TELE", name).map((s) => s.id)).toEqual(["telegram"]);
    expect(matching(services, "ads", name).map((s) => s.id)).toEqual(["ads"]);
    expect(matching(services, "", name)).toHaveLength(3);
  });
});
