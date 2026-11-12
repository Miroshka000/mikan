import { describe, expect, it } from "vitest";
import { KINDS, parseDoc, randomDoc, renderSite, type SiteDoc } from "./render";

const MAX = 200 << 10; // frontpage.MaxCustom

/** A document with the ids and dates of another: what differs then is the seed alone. */
const sameWords = (doc: SiteDoc, seed: string): SiteDoc => ({ ...doc, seed });

describe("the front page builder", () => {
  it("makes the same page of the same document", () => {
    const doc = randomDoc("installation-a", "ru");
    expect(renderSite(doc)).toBe(renderSite(JSON.parse(JSON.stringify(doc)) as SiteDoc));
  });

  it("gives every kind in both languages a whole page that asks nothing of the network", () => {
    for (const lang of ["ru", "en"] as const) {
      for (const kind of KINDS) {
        const doc = randomDoc(`seed-${kind}`, lang, kind);
        const html = renderSite(doc);
        expect(html.startsWith("<!doctype html>")).toBe(true);
        expect(html).toContain(`<html lang="${lang}">`);
        expect(html.match(/<h1>/g)).toHaveLength(1);
        expect(new TextEncoder().encode(html).length).toBeLessThan(MAX);
        // No script, no handler, nothing loaded from anywhere: the only addresses are the
        // page's own anchors and the icon drawn inside it.
        expect(html).not.toMatch(/<script|<iframe|<link rel="stylesheet"|@import|\son[a-z]+=|javascript:/i);
        for (const [, url] of html.matchAll(/(?:href|src)="([^"]*)"/g)) expect(url!.startsWith("#") || url!.startsWith("data:image/svg+xml,")).toBe(true);
        expect(html).not.toMatch(/url\(/);
        expect(doc.blocks.length).toBeGreaterThanOrEqual(3);
        expect(html).not.toContain("{name}");
        expect(html).not.toContain("undefined");
        expect(html).not.toContain("NaN");
      }
    }
  });

  it("serves other bytes for another seed, the words being the same", () => {
    const doc = randomDoc("installation-a", "en", "studio");
    const a = renderSite(doc);
    const b = renderSite(sameWords(doc, "installation-b"));
    expect(a).not.toBe(b);
    const classes = (html: string) => new Set([...html.matchAll(/class="([^"]+)"/g)].flatMap((m) => m[1]!.split(" ")));
    const [ca, cb] = [classes(a), classes(b)];
    const shared = [...ca].filter((c) => cb.has(c)).length;
    expect(shared).toBeLessThan(ca.size);
  });

  it("offers different sites to different installations", () => {
    const seen = new Set<string>();
    for (let i = 0; i < 40; i++) seen.add(randomDoc(`installation-${i}`, "ru").brand);
    expect(seen.size).toBeGreaterThan(12);
  });

  it("writes the admin's words as text, whatever they hold", () => {
    const doc = randomDoc("x", "en", "notes");
    doc.brand = `<img src=x onerror=alert(1)>`;
    doc.tag = `"><script>alert(2)</script>`;
    doc.blocks[0] = { id: "h", t: "hero", title: `</h1><script>alert(3)</script>`, text: `a & b < c` };
    doc.theme.palette.accent = `red;}body{display:none}`;
    const html = renderSite(doc);
    expect(html).not.toMatch(/<script|<img/i);
    expect(html).toContain("a &amp; b &lt; c");
    expect(html).not.toContain("display:none");
  });

  it("reads back only a document of its own", () => {
    const doc = randomDoc("x", "ru");
    expect(parseDoc(JSON.stringify(doc))?.brand).toBe(doc.brand);
    for (const bad of ["", "{", "{}", `{"v":2}`, `{"v":1,"blocks":[],"theme":{},"kind":"notes"}`, `{"v":1,"blocks":[],"theme":{"palette":{}},"kind":"shop"}`]) expect(parseDoc(bad)).toBeNull();
  });
});
