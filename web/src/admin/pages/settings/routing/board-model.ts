/** A service as a tile of the board. */
export type BoardService = { id: string; icon: string; name: string };
/** A column of the board: a target and the services sent there. */
export type BoardColumn = { target: string; label: string; items: BoardService[] };
export type Box = { left: number; right: number; top: number; bottom: number };

/**
 * The columns of the board, one per target in their order, each with the services sent there
 * in the catalog's order; a service left out of sent follows the mode (""). A target a service
 * is sent to that the board does not know (a node deleted since) gets a column of its own at
 * the end, named by missing: the service is shown where it is sent.
 */
export function boardColumns(services: BoardService[], sent: Record<string, string> | undefined, targets: { id: string; label: string }[], missing: (target: string) => string): BoardColumn[] {
  const known = new Set(targets.map((x) => x.id));
  const extra = [...new Set(Object.values(sent ?? {}).filter((to) => to && !known.has(to)))].map((id) => ({ id, label: missing(id) }));
  return [...targets, ...extra].map((x) => ({ target: x.id, label: x.label, items: services.filter((s) => (sent?.[s.id] ?? "") === x.id) }));
}

/** The services' targets with the service id sent to target; "" leaves it to the mode, as the server keeps it. */
export function moveService(sent: Record<string, string> | undefined, id: string, target: string): Record<string, string> {
  const next = { ...(sent ?? {}) };
  if (target) next[id] = target;
  else delete next[id];
  return next;
}

/** The index of the box the point (x, y) is in, the first of them; -1 for none. */
export function boxAt(x: number, y: number, boxes: (Box | undefined)[]): number {
  return boxes.findIndex((b) => !!b && x >= b.left && x <= b.right && y >= b.top && y <= b.bottom);
}

/** The services whose name or id has q in it, any case; all of them for an empty q. */
export function matching<T extends { id: string }>(services: T[], q: string, nameOf: (s: T) => string): T[] {
  const s = q.trim().toLowerCase();
  if (!s) return services;
  return services.filter((x) => nameOf(x).toLowerCase().includes(s) || x.id.includes(s));
}
