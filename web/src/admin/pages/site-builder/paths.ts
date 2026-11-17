// A value inside a block by its path ("title", "items.2.text"): what a text on the canvas
// and a field of the form both point at.

type Tree = Record<string, unknown> | unknown[];

export function getAt(root: unknown, path: string): unknown {
  let at: unknown = root;
  for (const key of path.split(".")) {
    if (at == null || typeof at !== "object") return undefined;
    at = (at as Record<string, unknown>)[key];
  }
  return at;
}

/** A copy of root with value at path; what the path does not touch is shared with root. */
export function setAt<T>(root: T, path: string, value: unknown): T {
  const [head, ...rest] = path.split(".");
  const key = head!;
  const copy = (Array.isArray(root) ? [...root] : { ...(root as object) }) as Tree;
  const next = rest.length === 0 ? value : setAt((copy as Record<string, unknown>)[key] ?? (/^\d+$/.test(rest[0]!) ? [] : {}), rest.join("."), value);
  (copy as Record<string, unknown>)[key] = next;
  return copy as T;
}
