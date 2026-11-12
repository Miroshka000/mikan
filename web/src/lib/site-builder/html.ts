import type { Rng } from "./rng";

/** Text as text: whatever the admin typed, a browser reads it as words. */
export const esc = (s: string) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);

/** A colour as the admin typed it, or the fallback: the styles take nothing else from a field. */
export const hex = (c: string, fallback: string) => (/^#[0-9a-f]{3,8}$/i.test(c ?? "") ? c : fallback);

/** A number for a drawing: one digit after the point is all a pixel needs. */
export const f1 = (n: number) => (Math.round(n * 10) / 10).toString();

const WORDS = [
  "box", "wrap", "lead", "card", "panel", "view", "tile", "unit", "cell", "slot", "area", "zone", "part", "block", "note", "info", "meta", "item", "row", "col",
  "main", "inner", "outer", "head", "foot", "side", "base", "core", "hub", "dock", "bay", "rack", "shelf", "desk", "pad", "tray", "list", "grid", "band", "strip",
  "copy", "text", "name", "mark", "sign", "top", "mid", "end", "pane", "sheet", "frame", "stack", "group", "set", "line", "bar", "rail", "deck", "plate", "field",
  "page", "site", "nav", "menu", "link", "btn", "tag", "pill", "badge", "icon", "pic", "media", "figure", "cap", "title", "sub", "intro", "body", "aside", "extra",
];

/**
 * The site's own names for its classes: words in the seed's order, some with a digit, some
 * with a prefix. Two installations name the same thing differently, so no rule of a
 * stylesheet and no class in the markup is shared between them.
 */
export class Namer {
  private readonly order: string[];
  private readonly prefix: string;
  private readonly made = new Map<string, string>();

  constructor(private readonly rng: Rng) {
    this.order = rng.shuffled(WORDS);
    this.prefix = rng.chance(0.4) ? rng.pick(["x", "s", "p", "u", "c", "m"]) + rng.pick(["-", "_", ""]) : "";
  }

  /** The class the site uses for key, the same at every call. */
  of(key: string): string {
    let v = this.made.get(key);
    if (!v) {
      const i = this.made.size;
      v = this.prefix + this.order[i % this.order.length]! + (i >= this.order.length || this.rng.chance(0.3) ? String(this.rng.int(1, 9)) : "");
      this.made.set(key, v);
    }
    return v;
  }
}
