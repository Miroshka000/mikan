import { describe, expect, it } from "vitest";
import type { Schemas, Tariff } from "../../../api/client";
import { renewsTraffic, resetOf, resetToSave } from "./model";

type Mode = Schemas["PoolSetting"]["mode"];

// The groups as the form holds them: a mode for each group, ids from 1.
const groups = (...modes: Mode[]) => Object.fromEntries(modes.map((mode, i) => [i + 1, { mode }]));
// A saved tariff: the fields the reset reads, a group of each mode given; the rest does not matter.
const tariff = (traffic_limit: number | undefined, reset_strategy: Tariff["reset_strategy"], ...modes: Mode[]) =>
  ({ traffic_limit, reset_strategy, pools: modes.map((mode, i) => ({ pool_id: i + 1, mode })) }) as Tariff;

describe("renewsTraffic", () => {
  it("renews a main traffic with a limit", () => {
    expect(renewsTraffic({ trafficLimited: true, groups: {} })).toBe(true);
    expect(renewsTraffic({ trafficLimited: true, groups: groups("unlimited") })).toBe(true);
  });
  it("renews a group with a limit of its own when the main traffic has none", () => {
    expect(renewsTraffic({ trafficLimited: false, groups: groups("main", "own") })).toBe(true);
  });
  it("has nothing to renew when nothing has a limit", () => {
    expect(renewsTraffic({ trafficLimited: false, groups: {} })).toBe(false);
    expect(renewsTraffic({ trafficLimited: false, groups: groups("main", "unlimited", "closed") })).toBe(false);
  });
});

describe("resetToSave", () => {
  it("keeps the reset chosen for the groups of a tariff with unlimited main traffic", () => {
    expect(resetToSave({ trafficLimited: false, groups: groups("own") }, "period")).toBe("period");
    expect(resetToSave({ trafficLimited: false, groups: groups("closed", "own") }, "month_start")).toBe("month_start");
  });
  it("keeps the reset chosen for a main traffic with a limit", () => {
    expect(resetToSave({ trafficLimited: true, groups: {} }, "month_start")).toBe("month_start");
    expect(resetToSave({ trafficLimited: true, groups: {} }, "none")).toBe("none");
  });
  it("saves none when nothing has a limit", () => {
    expect(resetToSave({ trafficLimited: false, groups: {} }, "period")).toBe("none");
    expect(resetToSave({ trafficLimited: false, groups: groups("main", "unlimited", "closed") }, "month_start")).toBe("none");
  });
});

describe("resetOf", () => {
  it("starts a new tariff from every 30 days", () => {
    expect(resetOf(null)).toBe("period");
  });
  it("shows the reset saved while something has a limit", () => {
    expect(resetOf(tariff(100, "none"))).toBe("none");
    expect(resetOf(tariff(100, "month_start", "unlimited"))).toBe("month_start");
    // Saved with never before the form asked for the groups: shown as it is, to be changed.
    expect(resetOf(tariff(undefined, "none", "main", "own"))).toBe("none");
    expect(resetOf(tariff(undefined, "month_start", "own"))).toBe("month_start");
  });
  it("starts a tariff with nothing limited from every 30 days: its none was nobody's choice", () => {
    expect(resetOf(tariff(undefined, "none"))).toBe("period");
    expect(resetOf(tariff(undefined, "none", "main", "unlimited", "closed"))).toBe("period");
  });
});
