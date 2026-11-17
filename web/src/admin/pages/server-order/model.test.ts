import { describe, expect, it } from "vitest";
import type { Inbound, Schemas } from "../../../api/client";
import { byExit, byNodes, inBlocks, rowsOf, sameOrder, type OrderItem } from "./model";

type Node = Schemas["NodeInfo"];

const item = (host_id: number, node_id: number, inbound_id: number, served = true): OrderItem => ({
  host_id,
  node_id,
  inbound_id,
  default: true,
  address: "",
  port: 0,
  cdn: false,
  name: `s${host_id}`,
  served,
  for_some: false,
});
const inbound = (id: number, node_id: number, extra: Partial<Inbound> = {}) => ({ id, node_id, enabled: true, ...extra }) as Inbound;
const node = (id: number, enabled = true) => ({ id, enabled }) as Node;

// NL (1) and DE (2) serve directly; RU (3) is a bridge whose protocols leave through NL and DE.
const nodes = [node(1), node(2), node(3)];
const inbounds = [inbound(10, 1), inbound(20, 2), inbound(30, 3, { exit_node_id: 1, outbound: "node" }), inbound(31, 3, { exit_node_id: 2, outbound: "node" }), inbound(32, 3)];
const items = [item(100, 3, 30), item(101, 1, 10), item(102, 3, 31), item(103, 2, 20), item(104, 1, 10), item(105, 3, 32)];
const rows = rowsOf(items, inbounds, nodes);
const ids = items.map((i) => i.host_id);

describe("server order", () => {
  it("groups the servers by node, in the nodes' order, each node's as they were", () => {
    expect(byNodes(ids, rows, [1, 2, 3])).toEqual([101, 104, 103, 100, 102, 105]);
    expect(byNodes(ids, rows, [3, 1])).toEqual([100, 102, 105, 101, 104, 103]);
  });

  it("puts a cascade next to its exit's own servers", () => {
    expect(byExit(ids, rows, [1, 2, 3])).toEqual([100, 101, 104, 102, 103, 105]);
    expect(byExit(ids, rows, [2, 1, 3])).toEqual([102, 103, 100, 101, 104, 105]);
  });

  it("tells why clients do not get a server", () => {
    const off = rowsOf(
      [item(1, 1, 10, false), item(2, 2, 20, false), item(3, 1, 11, false), item(4, 1, 11)],
      [inbound(10, 1, { enabled: false }), inbound(20, 2), inbound(11, 1)],
      [node(1), node(2, false)],
    );
    expect([1, 2, 3, 4].map((id) => off.get(id)?.off)).toEqual(["inbound", "node", "other", undefined]);
  });

  it("keeps a server whose protocol the list does not have yet", () => {
    const r = rowsOf([item(1, 9, 99)], [], []);
    expect(r.get(1)?.inbound).toBeUndefined();
    expect(byNodes([1], r, [1])).toEqual([1]);
  });

  it("knows servers that go node by node", () => {
    expect(inBlocks([1, 1, 2, 3, 3])).toBe(true);
    expect(inBlocks([])).toBe(true);
    expect(inBlocks([1, 2, 1])).toBe(false);
    expect(sameOrder([1, 2], [1, 2])).toBe(true);
    expect(sameOrder([1, 2], [2, 1])).toBe(false);
    expect(sameOrder([1], [1, 2])).toBe(false);
  });
});
