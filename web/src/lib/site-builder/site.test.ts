import { afterEach, describe, expect, it, vi } from "vitest";
import { BLOCK_KINDS, bundleSize, KINDS, parseDoc, SiteBundle, SiteGenerator, SiteRenderer, type Assets, type SiteDoc } from "./index";

const served: Assets = { font: (family, file) => `/fonts/${family.id}-${file.script}.woff2`, image: (id) => `/img/${id}.jpg` };
const make = (seed: string, lang: "ru" | "en", kind?: (typeof KINDS)[number]) => new SiteGenerator().random(seed, lang, kind);
const pagesOf = (doc: SiteDoc, edit = false) => {
  const site = new SiteRenderer(doc, { assets: served, edit });
  return [...doc.pages.map((p) => site.page(p)), site.notFound()];
};

afterEach(() => vi.unstubAllGlobals());

describe("the front page builder", () => {
  it("makes a site of several pages for every kind, in both languages", () => {
    for (const lang of ["ru", "en"] as const) {
      for (const kind of KINDS) {
        const doc = make(`seed-${kind}`, lang, kind);
        expect(doc.pages.length).toBeGreaterThanOrEqual(3);
        expect(doc.pages[0]!.path).toBe("");
        expect(doc.pages[0]!.blocks[0]!.t).toBe("hero");
        expect(new Set(doc.pages.map((p) => p.path)).size).toBe(doc.pages.length);
        for (const html of pagesOf(doc)) {
          expect(html.startsWith("<!doctype html>")).toBe(true);
          expect(html).toContain(`<html lang="${lang}">`);
          expect(html.match(/<h1/g)).toHaveLength(1);
          // No script, no handler, nothing asked of another host: the addresses are the
          // site's own pages and files, and the icon drawn inside the page.
          expect(/<script|<iframe|@import|\son[a-z]+=|javascript:|https?:\/\/(?!example\.org|www\.w3\.org)/i.test(html)).toBe(false);
          for (const [, url] of html.matchAll(/(?:href|src)="([^"]*)"/g)) expect(url!.startsWith("/") || url!.startsWith("data:image/svg+xml,")).toBe(true);
          for (const [, url] of html.matchAll(/url\("([^"]*)"\)/g)) expect(url!.startsWith("/fonts/")).toBe(true);
          for (const word of ["{name}", "undefined", "NaN", "data-b=", "data-e="]) expect(html).not.toContain(word);
        }
        // Every page of the menu is a page of the site.
        const home = pagesOf(doc)[0]!;
        for (const p of doc.pages) expect(home).toContain(`href="${p.path ? "/" + p.path : "/"}"`);
      }
    }
  });

  it("makes the same pages of the same document, and other bytes for another seed", () => {
    const doc = make("installation-a", "ru", "studio");
    expect(pagesOf(doc)).toEqual(pagesOf(JSON.parse(JSON.stringify(doc)) as SiteDoc));
    const other = pagesOf({ ...doc, seed: "installation-b" });
    expect(other[0]).not.toBe(pagesOf(doc)[0]);
    const classes = (html: string) => new Set([...html.matchAll(/class="([^"]+)"/g)].flatMap((m) => m[1]!.split(" ")));
    const [a, b] = [classes(pagesOf(doc)[0]!), classes(other[0]!)];
    expect([...a].filter((c) => b.has(c)).length).toBeLessThan(a.size);
  });

  it("offers different sites to different installations", () => {
    const seen = new Set<string>();
    for (let i = 0; i < 40; i++) seen.add(make(`installation-${i}`, "ru").brand);
    expect(seen.size).toBeGreaterThan(12);
  });

  it("marks blocks and texts for the editor only when asked", () => {
    const doc = make("x", "en", "club");
    const html = pagesOf(doc, true)[0]!;
    for (const b of doc.pages[0]!.blocks) expect(html).toContain(`data-b="${b.id}"`);
    expect(html).toContain('data-e="title"');
    expect(html).toContain(`data-go="${doc.pages[1]!.id}"`);
  });

  it("writes the admin's words as text, whatever they hold", () => {
    const doc = make("x", "en", "notes");
    doc.brand = `<img src=x onerror=alert(1)>`;
    doc.tag = `"><script>alert(2)</script>`;
    doc.pages[0]!.title = `<b>home</b>`;
    doc.pages[0]!.blocks[0] = { id: "h", t: "hero", title: `</h1><script>alert(3)</script>`, text: `a & b < c`, cta: `"go"`, page: `"><svg onload=1>`, image: "" };
    doc.pages[0]!.blocks.push({ id: "u", t: "unknown-kind", title: "x" });
    doc.theme.palette.accent = `red;}body{display:none}`;
    for (const kind of BLOCK_KINDS) doc.pages[1]!.blocks.push({ ...kind.create("en"), title: `<i>${kind.type}</i>` });
    for (const html of pagesOf(doc)) {
      expect(/<script|<img src=x|<svg onload|<i>|<b>home/i.test(html)).toBe(false);
      expect(html).not.toContain("display:none");
    }
    expect(pagesOf(doc)[0]).toContain("a &amp; b &lt; c");
  });

  it("packs the pages, a page for a missing address and the fonts, and a copy in one file", async () => {
    const fetched: string[] = [];
    vi.stubGlobal("fetch", async (url: string) => {
      fetched.push(url);
      return { ok: true, arrayBuffer: async () => new Uint8Array([1, 2, 3, 4]).buffer };
    });
    const doc = make("installation-a", "ru", "recipes");
    doc.images.pic = { type: "image/jpeg", data: "AAAA", w: 4, h: 3 };
    doc.pages[0]!.blocks[0]!.image = "pic";
    const { files, single } = await new SiteBundle(doc).build();
    const paths = files.map((f) => f.path);
    for (const p of doc.pages) expect(paths).toContain(p.path ? `${p.path}.html` : "index.html");
    expect(paths).toContain("404.html");
    const fonts = files.filter((f) => f.type === "font/woff2");
    expect(fonts.length).toBeGreaterThanOrEqual(2);
    expect(fetched).toHaveLength(fonts.length);
    const picture = files.find((f) => f.type === "image/jpeg")!;
    const index = atob(files.find((f) => f.path === "index.html")!.data);
    expect(index).toContain(`src="/${picture.path}"`);
    for (const f of fonts) expect(index).toContain(`url("/${f.path}")`);
    expect(new Set(paths).size).toBe(paths.length);
    for (const p of paths) expect(p).toMatch(/^[a-z0-9][a-z0-9/._-]*$/);
    expect(bundleSize(files)).toBeGreaterThan(1000);
    // The one-file copy asks for nothing: its picture is inside, its type is the visitor's.
    expect(single).toContain("data:image/jpeg;base64,AAAA");
    expect(single).not.toContain("@font-face");
    expect(single).not.toMatch(/src="\//);
  });

  it("reads back only a document of its own", () => {
    const doc = make("x", "ru");
    expect(parseDoc(JSON.stringify(doc))?.brand).toBe(doc.brand);
    for (const bad of ["", "{", "{}", `{"v":1,"blocks":[],"theme":{"palette":{}},"kind":"notes"}`, `{"v":2,"pages":[],"theme":{"palette":{},"layout":{}},"kind":"notes"}`, `{"v":2,"pages":[{"path":"","blocks":[]}],"theme":{"palette":{},"layout":{}},"kind":"shop"}`]) expect(parseDoc(bad)).toBeNull();
  });
});
