import { arr, str, type RenderContext, type StyleContext } from "./blocks/block";
import { BLOCK_KINDS, kindOf } from "./blocks/registry";
import { fontOf, type FontFamily, type FontFile } from "./fonts";
import { esc, hex, Namer } from "./html";
import type { BlockData, Page, Palette, SiteDoc, SiteImage } from "./model";
import { Rng } from "./rng";
import { palettesOf } from "./themes";

/**
 * Where a site's files are when a page asks for them. The editor points at the panel's own
 * copies; a saved site at its own addresses; the one-file copy carries pictures inside and
 * asks for no font at all.
 */
export interface Assets {
  /** The address of a font file, or null to leave the family to the visitor's own fonts. */
  font(family: FontFamily, file: FontFile): string | null;
  image(id: string, image: SiteImage): string;
}

export type RenderOptions = {
  assets: Assets;
  /** The editor's canvas: blocks and texts carry the marks it selects and edits them by. */
  edit?: boolean;
};

const NOT_FOUND = { ru: ["Страница не найдена", "Такой страницы здесь нет. Возможно, адрес набран с ошибкой.", "На главную"], en: ["Page not found", "There is no such page here. The address may be mistyped.", "To the first page"] };

/**
 * Makes the pages of a site of its document: the same bytes for the same document. Every
 * name in the markup and every choice in the rules comes from the document's seed, so
 * another installation's site shares none of them.
 */
export class SiteRenderer {
  private readonly names: Namer;
  private readonly palette: Palette;
  private readonly radius: number;
  private readonly width: number;
  private readonly unit: number;
  private readonly choice: Rng;
  private readonly wordmark: boolean;
  private readonly striped: boolean;
  private sheet?: string;

  constructor(
    private readonly doc: SiteDoc,
    private readonly opts: RenderOptions,
  ) {
    this.names = new Namer(new Rng("names:" + doc.seed));
    this.choice = new Rng("look:" + doc.seed);
    const d = palettesOf(doc.kind)[0]!;
    const p = doc.theme.palette;
    this.palette = { bg: hex(p.bg, d.bg), surface: hex(p.surface, d.surface), ink: hex(p.ink, d.ink), muted: hex(p.muted, d.muted), line: hex(p.line, d.line), accent: hex(p.accent, d.accent), on: hex(p.on, d.on) };
    this.radius = Math.min(28, Math.max(0, Math.round(doc.theme.radius) || 0));
    this.width = Math.min(1280, Math.max(600, Math.round(doc.theme.width) || 960));
    this.unit = this.choice.pick([20, 22, 24, 26]);
    this.wordmark = this.choice.chance(0.5);
    this.striped = this.choice.chance(0.4);
  }

  private cls = (key: string) => this.names.of(key);

  /** The address of a page of the site. */
  href(path: string): string {
    return path ? "/" + path : "/";
  }

  private context(block: BlockData): RenderContext {
    const edit = this.opts.edit;
    return {
      doc: { ...this.doc, theme: { ...this.doc.theme, palette: this.palette } },
      cls: this.cls,
      e: (path) => (edit ? ` data-e="${esc(path)}"` : ""),
      href: (path) => this.href(path),
      image: (id) => {
        const image = typeof id === "string" && id ? this.doc.images[id] : undefined;
        return image ? { src: this.opts.assets.image(id as string, image), w: image.w, h: image.h } : null;
      },
      rng: (key) => new Rng(`block:${this.doc.seed}:${block.id}:${key}`),
    };
  }

  /** The rules of the whole site: one sheet, the same in every page. */
  css(): string {
    if (this.sheet !== undefined) return this.sheet;
    const { cls, palette: p, unit, radius, width, choice: r } = this;
    const heading = fontOf(this.doc.theme.heading);
    const body = fontOf(this.doc.theme.body);
    const mono = fontOf("jetbrains").fallback;
    const layout = this.doc.theme.layout;
    const gap = r.pick([56, 64, 72, 80]);
    const style: StyleContext = { cls, rng: new Rng("blocks:" + this.doc.seed), theme: this.doc.theme, unit, radius, heading: heading.stack, mono };

    const faces = [...new Set([heading, body])].flatMap((family) =>
      family.files(this.doc.lang).flatMap((file) => {
        const url = this.opts.assets.font(family, file);
        return url ? [`@font-face{font-family:"${family.name}";font-style:normal;font-weight:${family.weights};font-display:swap;src:url("${url}") format("woff2");unicode-range:${file.range}}`] : [];
      }),
    );
    const base = [
      `.${cls("wrap")}{width:min(${width}px,100% - ${unit * 2}px);margin-inline:auto}`,
      `.${cls("head")}{border-bottom:1px solid var(--ln)}`,
      `.${cls("head")} .${cls("wrap")}{display:flex;flex-wrap:wrap;align-items:center;gap:${unit / 2}px ${unit}px;padding-block:${unit}px;${layout.header === "centered" ? "flex-direction:column;text-align:center" : "justify-content:space-between"}}`,
      `.${cls("brand")}{display:inline-flex;align-items:center;gap:10px;font-family:${heading.stack};font-weight:700;font-size:1.15em;color:var(--ink);text-decoration:none}`,
      `.${cls("brand")} svg{width:30px;height:30px;flex:none}`,
      `.${cls("nav")}{display:flex;flex-wrap:wrap;gap:4px ${unit}px;font-size:.92em}`,
      `.${cls("nav")} a{color:var(--mu);text-decoration:none;padding-block:10px}`,
      `.${cls("nav")} a:hover,.${cls("nav")} a:focus-visible,.${cls("nav")} a[aria-current]{color:var(--ink)}`,
      `.${cls("nav")} a[aria-current]{text-decoration:underline;text-underline-offset:6px;text-decoration-thickness:2px}`,
      `.${cls("sec")}{padding-block:${Math.round(gap * 0.8)}px}`,
      `.${cls("sec")}+.${cls("sec")}{border-top:1px solid var(--ln)}`,
      `.${cls("alt")}{background:var(--sf)}`,
      `.${cls("sec")} h2{font-family:${heading.stack};font-size:clamp(22px,3vw,30px);line-height:1.2;margin:0 0 ${unit}px;text-wrap:balance}`,
      `.${cls("aside")}{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,${r.pick(["2.2fr", "2.5fr", "2fr"])});gap:${unit}px ${unit * 2}px;align-items:start}`,
      `.${cls("aside")}>h2{position:sticky;top:${unit}px}`,
      `.${cls("top")}{padding-block:${gap}px ${Math.round(gap / 3)}px}`,
      `.${cls("top")} h1{font-family:${heading.stack};font-size:clamp(30px,5vw,44px);line-height:1.1;letter-spacing:-.015em;margin:0;text-wrap:balance}`,
      `h3{font-family:${heading.stack};font-size:1.08em;line-height:1.3;margin:0 0 6px}`,
      `.${cls("foot")}{border-top:1px solid var(--ln);color:var(--mu);font-size:.85em}`,
      `.${cls("foot")} .${cls("wrap")}{display:flex;flex-wrap:wrap;gap:8px ${unit}px;justify-content:space-between;padding-block:${unit * 1.5}px}`,
      `@media (max-width:${r.pick([700, 720, 760])}px){.${cls("split")},.${cls("aside")}{grid-template-columns:1fr}.${cls("aside")}>h2{position:static}}`,
    ];
    const blocks = BLOCK_KINDS.flatMap((kind) => kind.styles(style));
    // The rules in an order of the seed's own: the cascade does not depend on it, the bytes do.
    const rules = r.shuffled([...base.slice(0, -1), ...blocks.filter((x) => !x.startsWith("@media"))]);
    const media = [base.at(-1)!, ...blocks.filter((x) => x.startsWith("@media"))];
    this.sheet =
      faces.join("") +
      `:root{--bg:${p.bg};--sf:${p.surface};--ink:${p.ink};--mu:${p.muted};--ln:${p.line};--ac:${p.accent};--on:${p.on}}` +
      `*,*::before,*::after{box-sizing:border-box}html{-webkit-text-size-adjust:100%}` +
      `body{margin:0;background:var(--bg);color:var(--ink);font:${this.doc.theme.body === "jetbrains" ? 15 : 17}px/1.6 ${body.stack}}` +
      `a{color:inherit}a:focus-visible{outline:2px solid var(--ac);outline-offset:3px}img{max-width:100%}` +
      rules.join("") +
      media.join("") +
      `@media (prefers-reduced-motion:no-preference){html{scroll-behavior:smooth}}`;
    return this.sheet;
  }

  /** The brand's mark: its first letter in a shape of the seed's. */
  private mark(): string {
    const r = new Rng("mark:" + this.doc.seed);
    const letter = esc((this.doc.brand.replace(/[^\p{L}\p{N}]/gu, "")[0] ?? "•").toUpperCase());
    const shape = [`<rect x="2" y="2" width="28" height="28" rx="${r.int(0, 9)}"`, `<circle cx="16" cy="16" r="14"`, `<path d="M16 1 31 16 16 31 1 16Z"`][r.int(0, 2)]!;
    return `<svg viewBox="0 0 32 32" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">${shape} fill="${this.palette.accent}"/><text x="16" y="21.5" text-anchor="middle" font-family="Georgia,serif" font-size="16" font-weight="700" fill="${this.palette.on}">${letter}</text></svg>`;
  }

  private section(block: BlockData, index: number): string {
    const kind = kindOf(block);
    if (!kind) return "";
    const { cls } = this;
    const c = this.context(block);
    const body = kind.render(block, c);
    const mark = this.opts.edit ? ` data-b="${esc(block.id)}"` : "";
    if (block.t === "hero") {
      return `<section class="${cls("first")}${this.doc.theme.layout.hero === "band" ? " " + cls("band") : ""}"${mark}><div class="${cls("wrap")}">${body}</div></section>`;
    }
    const title = kind.titled && str(block.title).trim() ? `<h2${c.e("title")}>${esc(str(block.title))}</h2>` : "";
    const aside = title && this.doc.theme.layout.sections === "aside";
    const alt = this.striped && index % 2 === 1 ? " " + cls("alt") : "";
    return `<section class="${cls("sec")}${alt}"${mark}><div class="${cls("wrap")}${aside ? " " + cls("aside") : ""}">${title}${aside ? `<div>${body}</div>` : body}</div></section>`;
  }

  private shell(title: string, current: string | null, main: string): string {
    const { cls, doc } = this;
    const logo = this.mark();
    const nav = doc.pages.length > 1 ? `<nav class="${cls("nav")}" aria-label="${doc.lang === "ru" ? "Разделы" : "Sections"}">${doc.pages.map((p) => `<a href="${esc(this.href(p.path))}"${p.path === current ? ' aria-current="page"' : ""}${this.opts.edit ? ` data-go="${esc(p.id)}"` : ""}>${esc(p.title)}</a>`).join("")}</nav>` : "";
    const edit = (key: string) => (this.opts.edit ? ` data-s="${key}"` : "");
    return (
      `<!doctype html><html lang="${doc.lang}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
      `<title>${esc(title)}</title><meta name="description" content="${esc(doc.tag)}"><link rel="icon" href="data:image/svg+xml,${encodeURIComponent(logo)}"><style>${this.css()}</style></head>` +
      `<body><header class="${cls("head")}"><div class="${cls("wrap")}"><a class="${cls("brand")}" href="${this.href("")}">${this.wordmark ? "" : logo}<span${edit("brand")}>${esc(doc.brand)}</span></a>${nav}</div></header>` +
      `<main>${main}</main><footer class="${cls("foot")}"><div class="${cls("wrap")}"><span>© ${new Date().getFullYear()} ${esc(doc.brand)}</span><span${edit("foot")}>${esc(doc.foot)}</span></div></footer></body></html>`
    );
  }

  /** A page of the site, whole. */
  page(page: Page): string {
    const blocks = arr<BlockData>(page.blocks);
    const home = page.path === "";
    // A page with no first screen of its own starts with its name.
    const top = blocks.some((b) => b.t === "hero") ? "" : `<div class="${this.cls("wrap")} ${this.cls("top")}"><h1>${esc(home ? this.doc.brand : page.title)}</h1></div>`;
    const title = home ? this.doc.brand + (this.doc.tag ? " · " + this.doc.tag : "") : `${page.title} · ${this.doc.brand}`;
    return this.shell(title, page.path, top + blocks.map((b, i) => this.section(b, i)).join(""));
  }

  /** What the site answers for an address it does not have. */
  notFound(): string {
    const [title, text, back] = NOT_FOUND[this.doc.lang];
    const main = `<div class="${this.cls("wrap")} ${this.cls("top")}"><h1>${esc(title!)}</h1><p>${esc(text!)}</p><p><a href="${this.href("")}">${esc(back!)}</a></p></div>`;
    return this.shell(`${title} · ${this.doc.brand}`, null, main);
  }
}
