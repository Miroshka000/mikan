import { describe, expect, it } from "vitest";
import type { Schemas } from "../../api/client";
import { renewsTraffic, resetToSave, resetToSend, startReset } from "./reset-rules";

type Mode = Schemas["PoolSetting"]["mode"];

// The groups as a form holds them: a mode for each group, ids from 1.
const groups = (...modes: Mode[]) => Object.fromEntries(modes.map((mode, i) => [i + 1, { mode }]));

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

describe("resetToSend", () => {
  it("sends the client's new reset with a group's own limit on unlimited main traffic", () => {
    expect(resetToSend({ trafficLimited: false, groups: groups("own") }, "period", "none")).toBe("period");
    expect(resetToSend({ trafficLimited: true, groups: {} }, "none", "month_start")).toBe("none");
  });
  it("sends nothing when the reset did not change", () => {
    expect(resetToSend({ trafficLimited: false, groups: groups("own") }, "period", "period")).toBeUndefined();
  });
  it("sends nothing when nothing has a limit: the client's reset stays", () => {
    expect(resetToSend({ trafficLimited: false, groups: groups("main", "closed") }, "period", "month_start")).toBeUndefined();
  });
});

describe("startReset", () => {
  it("shows the reset saved while something has a limit", () => {
    expect(startReset({ trafficLimited: true, groups: {} }, "none")).toBe("none");
    expect(startReset({ trafficLimited: true, groups: groups("unlimited") }, "month_start")).toBe("month_start");
    // Saved with never before the forms asked for the groups: shown as it is, to be changed.
    expect(startReset({ trafficLimited: false, groups: groups("main", "own") }, "none")).toBe("none");
    expect(startReset({ trafficLimited: false, groups: groups("own") }, "month_start")).toBe("month_start");
  });
  it("starts from every 30 days where nothing is limited: that none was nobody's choice", () => {
    expect(startReset({ trafficLimited: false, groups: {} }, "none")).toBe("period");
    expect(startReset({ trafficLimited: false, groups: groups("main", "unlimited", "closed") }, "none")).toBe("period");
  });
  it("keeps a reset chosen where nothing is limited", () => {
    expect(startReset({ trafficLimited: false, groups: {} }, "month_start")).toBe("month_start");
  });
});
