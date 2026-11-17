// The document of the front page builder: what the admin edits and what is saved. A site
// is a few pages of blocks with one theme; the files a visitor gets are made of it
// (SiteRenderer, SiteBundle).

export type Lang = "ru" | "en";
export type Kind = "notes" | "tool" | "recipes" | "club" | "studio" | "gallery";
export const KINDS: Kind[] = ["notes", "tool", "recipes", "club", "studio", "gallery"];

export type ListStyle = "rows" | "cards" | "steps";

export type Palette = { bg: string; surface: string; ink: string; muted: string; line: string; accent: string; on: string };

export type FontId = "inter" | "manrope" | "rubik" | "lora" | "playfair" | "source-serif" | "jetbrains" | "system-sans" | "system-serif";

/** How the page is put together, apart from its colours and type. */
export type Layout = {
  /** The brand and the menu on one line, or the brand above the menu in the middle. */
  header: "bar" | "centered";
  /** A section's heading above its content, or beside it in a column of its own. */
  sections: "stack" | "aside";
  /** The first screen on the page's background, or on a band of the accent colour. */
  hero: "plain" | "band";
};

export type Theme = { palette: Palette; heading: FontId; body: FontId; radius: number; width: number; layout: Layout };

/** A block as it is saved: its kind (t) and whatever that kind keeps (blocks/*.ts). */
export type BlockData = { id: string; t: string } & Record<string, unknown>;

/** A page: path "" is the first one, any other is its address ("about" is /about). */
export type Page = { id: string; path: string; title: string; blocks: BlockData[] };

export type ImageType = "image/jpeg" | "image/png" | "image/webp";
/** A picture the admin added: its bytes in base64, as the browser shrank them. */
export type SiteImage = { type: ImageType; data: string; w: number; h: number };

export type SiteDoc = {
  v: 2;
  lang: Lang;
  kind: Kind;
  brand: string;
  tag: string;
  foot: string;
  /** What the markup and the drawings are made from: a new one changes every byte of the site. */
  seed: string;
  theme: Theme;
  pages: Page[];
  images: Record<string, SiteImage>;
};

let counter = 0;
/** An id for a block, a page or a picture: the editor's own, a visitor never sees it. */
export const newId = (prefix = "b") => `${prefix}${Date.now().toString(36)}${(counter++).toString(36)}`;

/** A saved document read back; null when it is not one this builder made. */
export function parseDoc(raw: string): SiteDoc | null {
  try {
    const d = JSON.parse(raw) as SiteDoc;
    if (d?.v !== 2 || !Array.isArray(d.pages) || d.pages.length === 0 || !d.theme?.palette || !d.theme.layout || !KINDS.includes(d.kind)) return null;
    if (!d.pages.every((p) => typeof p.path === "string" && Array.isArray(p.blocks))) return null;
    d.images ??= {};
    return d;
  } catch {
    return null;
  }
}

/** A page's address as it is typed, made one a site can have: latin letters, digits and dashes. */
export function slug(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
}
