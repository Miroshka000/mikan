import { newId, type BlockData, type Lang, type SiteDoc, type Theme } from "../model";
import type { Rng } from "../rng";

export type BlockGroup = "structure" | "content" | "business" | "media";

/**
 * A field of a block as the editor shows it. The label is the field's key (the editor has the
 * words for every key); an "items" field is a list of rows with fields of their own.
 */
export type FieldSpec =
  | { key: string; kind: "text" | "area"; max?: number }
  /** A list of strings: one a line ("lines"), or paragraphs with an empty line between ("paras"). */
  | { key: string; kind: "lines" | "paras"; mono?: boolean }
  | { key: string; kind: "choice"; options: string[] }
  /** A page of the site, for a link; "" is no link. */
  | { key: string; kind: "page" }
  /** A picture the admin added; "" is none. */
  | { key: string; kind: "image" }
  | { key: string; kind: "items"; of: FieldSpec[] };

/** What a block's markup is written with: the site's names, its links and its pictures. */
export interface RenderContext {
  readonly doc: SiteDoc;
  /** The site's class for key. */
  cls(key: string): string;
  /** In the editor: the attribute that makes the text at path (inside this block) editable in place; "" on a served page. */
  e(path: string): string;
  /** The address of a page of the site by its path. */
  href(path: string): string;
  /** A picture by its id: where it is served and its size; null when there is none. */
  image(id: unknown): { src: string; w: number; h: number } | null;
  /** Chance of this site for key: the same at every render. */
  rng(key: string): Rng;
}

/** What a block's rules are written with. */
export interface StyleContext {
  cls(key: string): string;
  readonly rng: Rng;
  readonly theme: Theme;
  /** The base step of the spacing, px. */
  readonly unit: number;
  readonly radius: number;
  /** font-family values. */
  readonly heading: string;
  readonly mono: string;
}

/**
 * A kind of block: what it keeps, how the editor shows its fields, and the markup and the
 * rules it adds to a page. One class a kind (the files beside this one), listed in registry.ts.
 */
export abstract class BlockKind<T extends BlockData = BlockData> {
  abstract readonly type: string;
  abstract readonly group: BlockGroup;
  abstract readonly fields: FieldSpec[];
  /** The renderer writes the block's `title` as the section's heading; false for a block that writes its own. */
  readonly titled: boolean = true;

  /** What a new block of the kind holds, in lang. */
  abstract empty(lang: Lang): Omit<T, "id" | "t">;
  /** The block's markup, without the section around it and without its heading. */
  abstract render(b: T, c: RenderContext): string;
  /** The rules the markup needs. Asked once a site, whether or not a page has the block. */
  abstract styles(s: StyleContext): string[];

  create(lang: Lang): T {
    return { id: newId(), t: this.type, ...this.empty(lang) } as T;
  }
}

/** A list read from a saved block: whatever is there, as an array. */
export const arr = <V>(v: unknown): V[] => (Array.isArray(v) ? (v as V[]) : []);
export const str = (v: unknown): string => (typeof v === "string" ? v : "");
