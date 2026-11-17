import type { Inbound, Schemas } from "../../../api/client";

type Node = Schemas["NodeInfo"];

/** One server of the subscriptions: an address for clients of a protocol. */
export type OrderItem = Schemas["SubOrderItem"];

/** Why clients get no server now: its protocol is off, its node is, or it does not build. */
export type Off = "inbound" | "node" | "other";

/** A server of the list with its protocol and node, as the page knows them. */
export type ServerRow = {
  item: OrderItem;
  inbound?: Inbound;
  node?: Node;
  /** The node its traffic leaves through: a cascade's exit, its own otherwise. */
  exit?: Node;
  off?: Off;
};

/** The servers' rows by address (host_id). A protocol or a node the lists have no more stays out. */
export function rowsOf(items: OrderItem[], inbounds: Inbound[], nodes: Node[]): Map<number, ServerRow> {
  const inboundById = new Map(inbounds.map((i) => [i.id, i]));
  const nodeById = new Map(nodes.map((n) => [n.id, n]));
  return new Map(
    items.map((item) => {
      const inbound = inboundById.get(item.inbound_id);
      const node = nodeById.get(item.node_id);
      const exit = inbound?.exit_node_id ? nodeById.get(inbound.exit_node_id) : node;
      return [item.host_id, { item, inbound, node, exit, off: offOf(item, inbound, node) }];
    }),
  );
}

function offOf(item: OrderItem, inbound: Inbound | undefined, node: Node | undefined): Off | undefined {
  if (item.served) return undefined;
  if (inbound && !inbound.enabled) return "inbound";
  if (node && !node.enabled) return "node";
  return "other";
}

/**
 * ids grouped by the node key gives each one, the groups in the nodes' order (a node it does
 * not list after them), each group in the order it had.
 */
function groupBy(ids: number[], key: (id: number) => number | undefined, nodeOrder: number[]): number[] {
  const rank = new Map(nodeOrder.map((id, i) => [id, i]));
  const of = (id: number) => {
    const node = key(id);
    return node === undefined ? nodeOrder.length : (rank.get(node) ?? nodeOrder.length);
  };
  return ids
    .map((id, i) => ({ id, i, r: of(id) }))
    .sort((a, b) => a.r - b.r || a.i - b.i)
    .map((x) => x.id);
}

/** The servers node by node in the nodes' order, a node's as they were. */
export function byNodes(ids: number[], rows: Map<number, ServerRow>, nodeOrder: number[]): number[] {
  return groupBy(ids, (id) => rows.get(id)?.item.node_id, nodeOrder);
}

/**
 * The servers by the node their traffic leaves through, in the nodes' order: a cascade next
 * to its exit's own servers, the rest as they were.
 */
export function byExit(ids: number[], rows: Map<number, ServerRow>, nodeOrder: number[]): number[] {
  return groupBy(ids, (id) => rows.get(id)?.exit?.id ?? rows.get(id)?.item.node_id, nodeOrder);
}

/** Whether every value of keys comes in one run: the servers of each node go as a block. */
export function inBlocks(keys: number[]): boolean {
  const seen = new Set<number>();
  for (let i = 0; i < keys.length; i++) {
    const k = keys[i]!;
    if (i > 0 && keys[i - 1] === k) continue;
    if (seen.has(k)) return false;
    seen.add(k);
  }
  return true;
}

/** Whether two orders are the same. */
export function sameOrder(a: readonly number[], b: readonly number[]): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}
