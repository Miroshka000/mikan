import type { FontFamily, FontFile } from "./fonts";
import type { ImageType, SiteDoc, SiteImage } from "./model";
import { SiteRenderer, type Assets } from "./renderer";
import { Rng } from "./rng";

/** A file of a saved site: its address inside the site, its type and its bytes in base64. */
export type SiteFileType = "text/html" | "font/woff2" | ImageType;
export type SiteFile = { path: string; type: SiteFileType; data: string };

const EXT: Record<string, string> = { "image/jpeg": "jpg", "image/png": "png", "image/webp": "webp" };

/** Bytes as base64, in pieces a browser's btoa can take. */
function base64(bytes: Uint8Array): string {
  let out = "";
  for (let i = 0; i < bytes.length; i += 0x6000) out += String.fromCharCode(...bytes.subarray(i, i + 0x6000));
  return btoa(out);
}
const textBase64 = (text: string) => base64(new TextEncoder().encode(text));

/**
 * The files of a site as the panel and its nodes serve them: a page an address, the fonts
 * and the pictures beside them, and a page for an address that is not there. The names of
 * the folders and of the files come from the site's seed, as everything else of it does.
 *
 * `single` is the first page alone, with its pictures inside and no font to fetch: what a
 * server that can hold one file shows (a node's protocol answering a stranger).
 */
export class SiteBundle {
  private readonly fontDir: string;
  private readonly imageDir: string;
  private readonly tail: string;
  private readonly fonts = new Map<string, string>();

  constructor(private readonly doc: SiteDoc) {
    const r = new Rng("files:" + doc.seed);
    this.fontDir = r.pick(["fonts", "assets/fonts", "static/fonts", "font"]);
    this.imageDir = r.pick(["img", "images", "media", "uploads", "assets/img"]);
    this.tail = Array.from({ length: 6 }, () => "0123456789abcdef"[r.int(0, 15)]).join("");
  }

  private fontPath(family: FontFamily, file: FontFile): string {
    return `${this.fontDir}/${family.id}-${file.script}.${this.tail}.woff2`;
  }

  private imagePath(id: string, image: SiteImage): string {
    const n = Object.keys(this.doc.images).indexOf(id) + 1;
    return `${this.imageDir}/${String(n).padStart(2, "0")}-${this.tail.slice(0, 4)}.${EXT[image.type] ?? "jpg"}`;
  }

  /** The files and the one-file copy; fetches the fonts the site is set in from the panel. */
  async build(): Promise<{ files: SiteFile[]; single: string }> {
    const used = new Set<string>();
    const served: Assets = {
      font: (family, file) => {
        this.fonts.set(this.fontPath(family, file), file.url);
        return "/" + this.fontPath(family, file);
      },
      image: (id, image) => {
        used.add(id);
        return "/" + this.imagePath(id, image);
      },
    };
    const site = new SiteRenderer(this.doc, { assets: served });
    const files: SiteFile[] = this.doc.pages.map((p) => ({ path: p.path === "" ? "index.html" : `${p.path}.html`, type: "text/html", data: textBase64(site.page(p)) }));
    files.push({ path: "404.html", type: "text/html", data: textBase64(site.notFound()) });
    for (const [path, url] of this.fonts) {
      const res = await fetch(url);
      if (!res.ok) throw new Error(`font ${url}: ${res.status}`);
      files.push({ path, type: "font/woff2", data: base64(new Uint8Array(await res.arrayBuffer())) });
    }
    for (const id of used) {
      const image = this.doc.images[id]!;
      files.push({ path: this.imagePath(id, image), type: image.type, data: image.data });
    }
    const alone = new SiteRenderer(this.doc, { assets: { font: () => null, image: (_, image) => `data:${image.type};base64,${image.data}` } });
    return { files, single: alone.page(this.doc.pages[0]!) };
  }
}

/** The size of the saved files as the server counts them: the bytes they decode to. */
export function bundleSize(files: SiteFile[]): number {
  return files.reduce((n, f) => n + Math.floor((f.data.length * 3) / 4), 0);
}
