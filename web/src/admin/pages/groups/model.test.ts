import { describe, expect, it } from "vitest";
import type { Inbound } from "../../../api/client";
import { groupHas, groupServersBody, groupServersDraft, type Group } from "./model";

// The fields the helpers read; the rest of a view does not matter to them.
const inbound = (id: number, node: number) => ({ id, node_id: node }) as Inbound;
const group = (servers: Group["servers"]) => ({ servers }) as Group;

describe("groupHas", () => {
  it("takes nothing while the group leaves the servers to the tariff", () => {
    expect(groupHas(group({ as_tariff: true, nodes: [1], inbounds: [5] }), inbound(5, 1))).toBe(false);
  });
  it("takes a node whole and a protocol alone", () => {
    const g = group({ as_tariff: false, nodes: [2], inbounds: [5] });
    expect(groupHas(g, inbound(1, 2))).toBe(true);
    expect(groupHas(g, inbound(5, 3))).toBe(true);
    expect(groupHas(g, inbound(6, 3))).toBe(false);
  });
});

describe("groupServersBody", () => {
  it("sends nothing chosen for as in the tariff", () => {
    expect(groupServersBody({ all: true, nodes: new Set([1]), inbounds: new Set([2]) })).toEqual({ as_tariff: true });
  });
  it("sends the chosen nodes and protocols", () => {
    expect(groupServersBody({ all: false, nodes: new Set([1]), inbounds: new Set([2, 3]) })).toEqual({ as_tariff: false, nodes: [1], inbounds: [2, 3] });
  });
  it("round-trips a saved group through the form", () => {
    const saved = group({ as_tariff: false, nodes: [4], inbounds: [7] });
    expect(groupServersBody(groupServersDraft(saved))).toEqual({ as_tariff: false, nodes: [4], inbounds: [7] });
    expect(groupServersDraft().all).toBe(true);
  });
});
