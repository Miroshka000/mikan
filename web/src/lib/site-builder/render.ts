// The front page builder: a document (what the admin edits), a random document for a seed,
// and the page made of a document. The page is one HTML file with its styles and drawings
// inside: no script, no font, no image and no request to anywhere, so it loads the same
// from the panel and from a node, and a visitor's browser tells nobody it was opened.
//
// Everything a visitor's browser receives depends on the document's seed: the names of the
// classes, the order of the rules, the spacing, the drawings. Two installations that press
// the same buttons still serve different bytes, and no hash finds them both.

import { BANKS, KINDS, MONTHS, type Kind, type Lang, type ListStyle, type SectionBank } from "./content";

export type { Kind, Lang, ListStyle };
export { KINDS };

export type ListItem = { title: string; text: string; meta?: string };
export type TableRow = { name: string; note: string; value: string };

export type Block =
  | { id: string; t: "hero"; title: string; text: string }
  | { id: string; t: "list"; title: string; style: ListStyle; items: ListItem[] }
  | { id: string; t: "prose"; title: string; paras: string[] }
  | { id: string; t: "table"; title: string; rows: TableRow[] }
  | { id: string; t: "gallery"; title: string; works: string[] }
  | { id: string; t: "code"; title: string; lines: string[] };

export type BlockType = Block["t"];
export const BLOCK_TYPES: BlockType[] = ["hero", "list", "prose", "table", "gallery", "code"];

export type Palette = { bg: string; surface: string; ink: string; muted: string; line: string; accent: string; on: string };
export type FontId = "serif" | "sans" | "mono" | "slab" | "humanist";
export const FONTS: FontId[] = ["serif", "sans", "mono", "slab", "humanist"];
export type Theme = { palette: Palette; heading: FontId; body: FontId; radius: number; width: number };

export type SiteDoc = {
  v: 1;
  lang: Lang;
  kind: Kind;
  brand: string;
  tag: string;
  foot: string;
  /** What the markup and the drawings are made from: a new one changes every byte of the page. */
  seed: string;
  theme: Theme;
  blocks: Block[];
};

// --- chance, the same for the same seed

export type Rng = () => number;

export function rngOf(seed: string): Rng {
  let h = 1779033703 ^ seed.length;
  for (let i = 0; i < seed.length; i++) {
    h = Math.imul(h ^ seed.charCodeAt(i), 3432918353);
    h = (h << 13) | (h >>> 19);
  }
  let a = h >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const int = (r: Rng, lo: number, hi: number) => lo + Math.floor(r() * (hi - lo + 1));
const pick = <T,>(r: Rng, list: readonly T[]): T => list[Math.floor(r() * list.length)]!;
function shuffled<T>(r: Rng, list: readonly T[]): T[] {
  const out = [...list];
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(r() * (i + 1));
    [out[i], out[j]] = [out[j]!, out[i]!];
  }
  return out;
}
const take = <T,>(r: Rng, list: readonly T[], [lo, hi]: [number, number]) => shuffled(r, list).slice(0, Math.min(list.length, int(r, lo, hi)));

// --- the looks of each kind of site, taken from what the site is about

const PALETTES: Record<Kind, Palette[]> = {
  notes: [
    { bg: "#fbf8f1", surface: "#f3eee2", ink: "#22201b", muted: "#6a6457", line: "#ddd5c3", accent: "#9a3b2a", on: "#ffffff" },
    { bg: "#f7f8f6", surface: "#eceee9", ink: "#1c2420", muted: "#5c675f", line: "#d3d8d0", accent: "#1f5a8a", on: "#ffffff" },
    { bg: "#fffdf8", surface: "#f4f1e8", ink: "#1d241d", muted: "#5f6a5e", line: "#dedad0", accent: "#2f5d3a", on: "#ffffff" },
  ],
  tool: [
    { bg: "#101418", surface: "#171c22", ink: "#e3e8ee", muted: "#94a0ad", line: "#2a323b", accent: "#7bd389", on: "#0c1410" },
    { bg: "#f6f6f3", surface: "#ecece7", ink: "#1b1d1f", muted: "#5d6368", line: "#d5d5cd", accent: "#a84a06", on: "#ffffff" },
    { bg: "#14120f", surface: "#1d1a16", ink: "#ece6da", muted: "#a39b8c", line: "#322d26", accent: "#e9b44c", on: "#1a1408" },
  ],
  recipes: [
    { bg: "#fdf6ec", surface: "#f6ead8", ink: "#2b1d14", muted: "#75604f", line: "#e6d5bd", accent: "#b8401f", on: "#ffffff" },
    { bg: "#f5f4ea", surface: "#e9e8d8", ink: "#232a1b", muted: "#5f6851", line: "#d5d6bf", accent: "#4f6a25", on: "#ffffff" },
    { bg: "#fff8f0", surface: "#fbeadb", ink: "#2a1a1a", muted: "#765a55", line: "#eed7c4", accent: "#a3352f", on: "#ffffff" },
  ],
  club: [
    { bg: "#f3f0e6", surface: "#e7e2d2", ink: "#17241d", muted: "#55635a", line: "#cfc9b5", accent: "#1d6b4a", on: "#ffffff" },
    { bg: "#f1f3f8", surface: "#e2e6f0", ink: "#141c33", muted: "#545d78", line: "#c9cfdf", accent: "#b93c0b", on: "#ffffff" },
    { bg: "#fbf3df", surface: "#f3e6c4", ink: "#261d0d", muted: "#6c5d3f", line: "#e2d2a8", accent: "#1e4e79", on: "#ffffff" },
  ],
  studio: [
    { bg: "#f4eee6", surface: "#e9dfd2", ink: "#2a221d", muted: "#6f6258", line: "#d8cbbb", accent: "#a04627", on: "#ffffff" },
    { bg: "#eeece7", surface: "#e1ded6", ink: "#23262a", muted: "#5f646b", line: "#cfcbc1", accent: "#3d5a66", on: "#ffffff" },
    { bg: "#f7f1e8", surface: "#ecdfcc", ink: "#2b2118", muted: "#73624f", line: "#dcc9ae", accent: "#7a5a1e", on: "#ffffff" },
  ],
  gallery: [
    { bg: "#fafafa", surface: "#f0f0f0", ink: "#111111", muted: "#5f5f5f", line: "#d9d9d9", accent: "#111111", on: "#ffffff" },
    { bg: "#0e0e0e", surface: "#171717", ink: "#ededed", muted: "#9a9a9a", line: "#2b2b2b", accent: "#ededed", on: "#0e0e0e" },
    { bg: "#f2efe9", surface: "#e6e2d9", ink: "#1a1a1a", muted: "#63605a", line: "#d2cdc1", accent: "#c2301a", on: "#ffffff" },
  ],
};

export const palettesOf = (kind: Kind): Palette[] => PALETTES[kind];

const LOOKS: Record<Kind, { fonts: [FontId, FontId][]; radius: number[]; width: number[] }> = {
  notes: { fonts: [["serif", "serif"], ["serif", "sans"]], radius: [0, 2], width: [720, 760] },
  tool: { fonts: [["mono", "mono"], ["mono", "sans"]], radius: [0, 4, 6], width: [860, 920] },
  recipes: { fonts: [["slab", "sans"], ["serif", "sans"]], radius: [8, 14], width: [1000, 1080] },
  club: { fonts: [["humanist", "humanist"], ["slab", "sans"]], radius: [4, 10], width: [1000, 1080] },
  studio: { fonts: [["serif", "sans"], ["serif", "humanist"]], radius: [2, 6], width: [1040, 1120] },
  gallery: { fonts: [["sans", "sans"], ["mono", "sans"]], radius: [0], width: [1120, 1200] },
};

const STACKS: Record<FontId, string> = {
  serif: '"Iowan Old Style","Palatino Linotype",Palatino,"Book Antiqua",Georgia,serif',
  sans: 'system-ui,-apple-system,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif',
  mono: 'ui-monospace,"SF Mono","Cascadia Mono",Consolas,"Liberation Mono",Menlo,monospace',
  slab: 'Rockwell,"Roboto Slab","Sitka Text","DejaVu Serif",Georgia,serif',
  humanist: '"Gill Sans","Gill Sans MT",Calibri,"Trebuchet MS",Verdana,sans-serif',
};

// --- a random document

let counter = 0;
/** A block's id: for the editor's lists only, the page never shows it. */
export const blockId = () => `b${Date.now().toString(36)}${(counter++).toString(36)}`;

/** A seed nobody else has: the installation's own and what the browser draws at random. */
export function freshSeed(installation: string): string {
  const raw = new Uint32Array(4);
  crypto.getRandomValues(raw);
  return installation.slice(0, 12) + Array.from(raw, (n) => n.toString(36)).join("");
}

function dates(r: Rng, lang: Lang, n: number): string[] {
  const out: string[] = [];
  const at = new Date();
  at.setDate(at.getDate() - int(r, 4, 25));
  for (let i = 0; i < n; i++) {
    const month = MONTHS[lang][at.getMonth()]!;
    out.push(`${at.getDate()} ${month} ${at.getFullYear()}`);
    at.setDate(at.getDate() - int(r, 9, 47));
  }
  return out;
}

function section(r: Rng, lang: Lang, s: SectionBank, name: string): Block {
  const title = pick(r, s.title);
  const named = (text: string) => text.replaceAll("{name}", name);
  switch (s.t) {
    case "list": {
      // Steps follow one another: those taken keep the order they were written in.
      const items = s.style === "steps" ? take(r, s.pool.map((it, i) => [i, it] as const), s.take).sort((a, b) => a[0] - b[0]).map(([, it]) => it) : take(r, s.pool, s.take);
      const when = s.dated ? dates(r, lang, items.length) : [];
      return { id: blockId(), t: "list", title, style: s.style, items: items.map(([t, text, meta], i) => ({ title: t, text, meta: when[i] ?? meta })) };
    }
    case "prose":
      return { id: blockId(), t: "prose", title, paras: shuffled(r, s.pool).slice(0, s.take) };
    case "table": {
      // A table reads in the order it was written (a changelog, a schedule): a run of it, not a shuffle.
      const n = int(r, s.take[0], s.take[1]);
      const from = int(r, 0, Math.max(0, s.pool.length - n));
      return { id: blockId(), t: "table", title, rows: s.pool.slice(from, from + n).map(([n1, note, value]) => ({ name: n1, note, value })) };
    }
    case "gallery":
      return { id: blockId(), t: "gallery", title, works: take(r, s.pool, s.take) };
    case "code":
      return { id: blockId(), t: "code", title, lines: pick(r, s.pool).map(named) };
    case "facts":
      return { id: blockId(), t: "table", title, rows: take(r, s.pool, s.take).map(([k, v]) => ({ name: k, note: "", value: v })) };
  }
}

/** A whole site for seed: its kind (unless given), its name, words, looks and sections. */
export function randomDoc(seed: string, lang: Lang, kind?: Kind): SiteDoc {
  const r = rngOf("doc:" + seed);
  const k = kind ?? pick(r, KINDS);
  const bank = BANKS[lang][k];
  const brand = Array.isArray(bank.names[0]) ? pick(r, bank.names[0] as string[]) + pick(r, bank.names[1] as string[]) : pick(r, bank.names as string[]);
  const [title, text] = pick(r, bank.heroes);
  const look = LOOKS[k];
  const [heading, body] = pick(r, look.fonts);
  // The first section stays first (it is what the first screen points at), the rest change places.
  const [first, ...rest] = bank.sections;
  const order = first ? [first, ...shuffled(r, rest)] : [];
  return {
    v: 1,
    lang,
    kind: k,
    brand,
    tag: pick(r, bank.tags),
    foot: pick(r, bank.foot),
    seed,
    theme: { palette: { ...pick(r, PALETTES[k]) }, heading, body, radius: pick(r, look.radius), width: pick(r, look.width) },
    blocks: [{ id: blockId(), t: "hero", title: title.replaceAll("{name}", brand), text }, ...order.map((s) => section(r, lang, s, brand))],
  };
}

/** An empty block of type for the editor's "add". */
export function emptyBlock(t: BlockType, lang: Lang): Block {
  const w = lang === "ru" ? { title: "Заголовок", text: "Текст", name: "Название", note: "пояснение", value: "значение" } : { title: "Heading", text: "Text", name: "Name", note: "note", value: "value" };
  switch (t) {
    case "hero":
      return { id: blockId(), t, title: w.title, text: w.text };
    case "list":
      return { id: blockId(), t, title: w.title, style: "cards", items: [{ title: w.title, text: w.text }] };
    case "prose":
      return { id: blockId(), t, title: w.title, paras: [w.text] };
    case "table":
      return { id: blockId(), t, title: w.title, rows: [{ name: w.name, note: w.note, value: w.value }] };
    case "gallery":
      return { id: blockId(), t, title: w.title, works: [w.name, w.name, w.name] };
    case "code":
      return { id: blockId(), t, title: w.title, lines: ["$ echo hello"] };
  }
}

/** A saved document read back; null when it is not one this builder made. */
export function parseDoc(raw: string): SiteDoc | null {
  try {
    const d = JSON.parse(raw) as SiteDoc;
    if (d?.v !== 1 || !Array.isArray(d.blocks) || !d.theme?.palette || !KINDS.includes(d.kind)) return null;
    return d;
  } catch {
    return null;
  }
}

// --- the page

const esc = (s: string) => s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
/** A colour as the admin typed it, or the fallback: the page's styles take nothing else from a field. */
const hex = (c: string, fallback: string) => (/^#[0-9a-f]{3,8}$/i.test(c) ? c : fallback);
const f1 = (n: number) => (Math.round(n * 10) / 10).toString();

const WORDS = [
  "box", "wrap", "lead", "card", "panel", "view", "tile", "unit", "cell", "slot", "area", "zone", "part", "block", "note", "info", "meta", "item", "row", "col",
  "main", "inner", "outer", "head", "foot", "side", "base", "core", "hub", "dock", "bay", "rack", "shelf", "desk", "pad", "tray", "list", "grid", "band", "strip",
  "copy", "text", "name", "mark", "sign", "top", "mid", "end", "pane", "sheet", "frame", "stack", "group", "set", "line", "bar", "rail", "deck", "plate", "field",
];

/** The page's own names for its classes: words in the seed's order, some with a digit or a second word. */
function namer(r: Rng): (key: string) => string {
  const order = shuffled(r, WORDS);
  const sep = pick(r, ["-", "_", ""]);
  const prefix = r() < 0.4 ? pick(r, ["x", "s", "p", "u", "c", "m"]) + sep : "";
  const made = new Map<string, string>();
  return (key) => {
    let v = made.get(key);
    if (!v) {
      const i = made.size;
      v = prefix + order[i % order.length]! + (i >= order.length || r() < 0.3 ? String(int(r, 1, 9)) : "");
      made.set(key, v);
    }
    return v;
  };
}

type Art = { ink: string; accent: string; surface: string; line: string };

function svg(body: string, label: string, w = 400, h = 300): string {
  return `<svg viewBox="0 0 ${w} ${h}" role="img" aria-label="${esc(label)}" xmlns="http://www.w3.org/2000/svg" fill="none" stroke-linecap="round" stroke-linejoin="round">${body}</svg>`;
}

/** A plotter drawing: one of a few rules, every number from r. */
function plot(r: Rng, a: Art, w = 400, h = 300): string {
  const kind = int(r, 0, 3);
  let out = `<rect width="${w}" height="${h}" fill="${a.surface}"/>`;
  if (kind === 0) {
    // drifting horizontal lines
    const rows = int(r, 14, 26);
    const amp = 4 + r() * 16;
    const freq = 0.01 + r() * 0.03;
    const phase = r() * 6;
    for (let i = 1; i < rows; i++) {
      const y = (h / rows) * i;
      let d = "";
      for (let x = 24; x <= w - 24; x += 8) {
        const yy = y + Math.sin(x * freq + phase + i * 0.35) * amp * Math.sin((i / rows) * Math.PI);
        d += (d ? "L" : "M") + f1(x) + " " + f1(yy);
      }
      out += `<path d="${d}" stroke="${i % int(r, 5, 9) === 0 ? a.accent : a.ink}" stroke-width="${r() < 0.2 ? 1.6 : 0.9}"/>`;
    }
  } else if (kind === 1) {
    // a grid with a fault
    const n = int(r, 7, 12);
    const m = Math.round(n * (h / w));
    const cw = (w - 48) / n;
    const fx = int(r, 1, n - 2);
    const fy = int(r, 1, m - 2);
    for (let i = 0; i < n; i++)
      for (let j = 0; j < m; j++) {
        const far = Math.hypot(i - fx, j - fy);
        const turn = far < 3 ? (3 - far) * (8 + r() * 14) : r() * 2;
        const s = cw * (0.55 + r() * 0.2);
        const cx = 24 + cw * (i + 0.5);
        const cy = 24 + cw * (j + 0.5);
        out += `<rect x="${f1(cx - s / 2)}" y="${f1(cy - s / 2)}" width="${f1(s)}" height="${f1(s)}" transform="rotate(${f1(turn)} ${f1(cx)} ${f1(cy)})" stroke="${far < 1 ? a.accent : a.ink}" stroke-width="1"/>`;
      }
  } else if (kind === 2) {
    // rings off centre
    const cx = w * (0.3 + r() * 0.4);
    const cy = h * (0.3 + r() * 0.4);
    const n = int(r, 9, 18);
    for (let i = 1; i <= n; i++) {
      const rad = (Math.max(w, h) * 0.62 * i) / n;
      out += `<circle cx="${f1(cx + i * (r() - 0.5) * 3)}" cy="${f1(cy + i * (r() - 0.5) * 3)}" r="${f1(rad)}" stroke="${i === int(r, 2, n) ? a.accent : a.ink}" stroke-width="0.9"${r() < 0.3 ? ` stroke-dasharray="${int(r, 2, 9)} ${int(r, 3, 8)}"` : ""}/>`;
    }
  } else {
    // hatching in bands
    const bands = int(r, 3, 6);
    const bh = (h - 48) / bands;
    for (let b = 0; b < bands; b++) {
      const step = 4 + r() * 9;
      const lean = (r() - 0.5) * bh * 1.4;
      for (let x = 24; x < w - 24; x += step) {
        out += `<path d="M${f1(x)} ${f1(24 + bh * b + 4)}L${f1(Math.min(w - 24, Math.max(24, x + lean)))} ${f1(24 + bh * (b + 1) - 4)}" stroke="${b === 1 && r() < 0.5 ? a.accent : a.ink}" stroke-width="0.9"/>`;
      }
    }
  }
  return out;
}

/** A thrown vessel: a profile turned around its axis, from r. */
function vessel(r: Rng, a: Art, cx: number, base: number, scale: number, glaze: string): string {
  const hgt = (70 + r() * 90) * scale;
  const foot = (16 + r() * 14) * scale;
  const belly = (30 + r() * 34) * scale;
  const neck = (10 + r() * 26) * scale;
  const lip = neck + (r() * 10 - 2) * scale;
  const by = base - hgt * (0.3 + r() * 0.25);
  const top = base - hgt;
  // The outline: right side up, across the lip, left side down, closed along the foot.
  const right =`M${f1(cx + foot)} ${f1(base)}C${f1(cx + belly * 1.25)} ${f1(base - hgt * 0.08)} ${f1(cx + belly * 1.2)} ${f1(by)} ${f1(cx + belly)} ${f1(by - hgt * 0.12)}C${f1(cx + belly * 0.8)} ${f1(top + hgt * 0.3)} ${f1(cx + neck)} ${f1(top + hgt * 0.22)} ${f1(cx + lip)} ${f1(top)}`;
  const fill = `${right}L${f1(cx - lip)} ${f1(top)}C${f1(cx - neck)} ${f1(top + hgt * 0.22)} ${f1(cx - belly * 0.8)} ${f1(top + hgt * 0.3)} ${f1(cx - belly)} ${f1(by - hgt * 0.12)}C${f1(cx - belly * 1.2)} ${f1(by)} ${f1(cx - belly * 1.25)} ${f1(base - hgt * 0.08)} ${f1(cx - foot)} ${f1(base)}Z`;
  const band = top + hgt * (0.35 + r() * 0.3);
  return (
    `<path d="${fill}" fill="${glaze}" stroke="${a.ink}" stroke-width="1.6"/>` +
    `<ellipse cx="${f1(cx)}" cy="${f1(top)}" rx="${f1(Math.abs(lip))}" ry="${f1(Math.abs(lip) * 0.22)}" fill="${a.surface}" stroke="${a.ink}" stroke-width="1.6"/>` +
    (r() < 0.6 ? `<path d="M${f1(cx - belly * 0.7)} ${f1(band)}Q${f1(cx)} ${f1(band + 9 * scale)} ${f1(cx + belly * 0.7)} ${f1(band)}" stroke="${a.ink}" stroke-width="1.2"/>` : "")
  );
}

function pots(r: Rng, a: Art): string {
  let out = `<rect width="400" height="300" fill="${a.surface}"/><path d="M24 250H376" stroke="${a.ink}" stroke-width="1.6"/>`;
  const n = int(r, 3, 4);
  for (let i = 0; i < n; i++) out += vessel(r, a, 70 + (260 / (n - 1)) * i + (r() - 0.5) * 16, 250, 0.9 + r() * 0.25, i === int(r, 0, n - 1) ? a.accent : a.line);
  return out;
}

function bowl(r: Rng, a: Art): string {
  let out = `<rect width="400" height="300" fill="${a.surface}"/>`;
  const cx = 200 + (r() - 0.5) * 30;
  const rim = 150 + (r() - 0.5) * 14;
  const wide = 110 + r() * 24;
  const deep = 70 + r() * 26;
  for (let i = 0; i < 3; i++) {
    const x = cx - 40 + i * 40 + (r() - 0.5) * 10;
    out += `<path d="M${f1(x)} ${f1(rim - 22)}c${f1(-10 - r() * 6)} -18 ${f1(10 + r() * 6)} -30 0 -${f1(48 + r() * 14)}" stroke="${a.ink}" stroke-width="1.5"/>`;
  }
  out += `<path d="M${f1(cx - wide)} ${f1(rim)}Q${f1(cx - wide * 0.86)} ${f1(rim + deep)} ${f1(cx - 34)} ${f1(rim + deep)}H${f1(cx + 34)}Q${f1(cx + wide * 0.86)} ${f1(rim + deep)} ${f1(cx + wide)} ${f1(rim)}Z" fill="${a.accent}" stroke="${a.ink}" stroke-width="1.8"/>`;
  out += `<ellipse cx="${f1(cx)}" cy="${f1(rim)}" rx="${f1(wide)}" ry="${f1(14 + r() * 5)}" fill="${a.line}" stroke="${a.ink}" stroke-width="1.8"/>`;
  out += `<path d="M${f1(cx - 44)} ${f1(rim + deep)}V${f1(rim + deep + 9)}H${f1(cx + 44)}V${f1(rim + deep)}" stroke="${a.ink}" stroke-width="1.8"/>`;
  // a spoon and what waits beside the bowl
  const sx = cx + wide + 28;
  out += `<path d="M${f1(sx)} ${f1(rim + deep + 6)}L${f1(sx + 34)} ${f1(rim - 30)}" stroke="${a.ink}" stroke-width="2.2"/><ellipse cx="${f1(sx + 39)}" cy="${f1(rim - 42)}" rx="9" ry="14" transform="rotate(24 ${f1(sx + 39)} ${f1(rim - 42)})" fill="${a.surface}" stroke="${a.ink}" stroke-width="1.8"/>`;
  for (let i = 0; i < int(r, 2, 4); i++) {
    const x = cx - wide - 16 - i * 22 - r() * 6;
    const rad = 9 + r() * 7;
    out += `<circle cx="${f1(x)}" cy="${f1(rim + deep + 9 - rad)}" r="${f1(rad)}" fill="${i % 2 ? a.line : a.surface}" stroke="${a.ink}" stroke-width="1.6"/>`;
  }
  out += `<path d="M24 ${f1(rim + deep + 9)}H376" stroke="${a.ink}" stroke-width="1.6"/>`;
  return out;
}

function board(r: Rng, a: Art): string {
  let out = `<rect width="400" height="300" fill="${a.surface}"/>`;
  // a track of squares that turns a corner
  const size = 34;
  const cells: [number, number][] = [];
  const turn = int(r, 4, 6);
  for (let i = 0; i < 9; i++) cells.push(i < turn ? [48 + i * (size + 4), 196] : [48 + (turn - 1) * (size + 4), 196 - (i - turn + 1) * (size + 4)]);
  cells.forEach(([x, y], i) => {
    out += `<rect x="${x}" y="${y}" width="${size}" height="${size}" rx="4" fill="${i % int(r, 3, 4) === 0 ? a.accent : a.line}" stroke="${a.ink}" stroke-width="1.5"/>`;
  });
  // pawns on the track
  for (const at of [int(r, 0, 2), int(r, 4, 8)]) {
    const [x, y] = cells[at]!;
    const cx = x + size / 2;
    out += `<path d="M${f1(cx - 11)} ${y + 20}L${f1(cx - 5)} ${y - 6}H${f1(cx + 5)}L${f1(cx + 11)} ${y + 20}Z" fill="${a.surface}" stroke="${a.ink}" stroke-width="1.6"/><circle cx="${f1(cx)}" cy="${y - 13}" r="8" fill="${a.surface}" stroke="${a.ink}" stroke-width="1.6"/>`;
  }
  // two dice
  for (let d = 0; d < 2; d++) {
    const x = 268 + d * 54 + r() * 6;
    const y = 150 + d * 34 + r() * 8;
    const rot = (r() - 0.5) * 40;
    const pips = [[0.5, 0.5], [0.25, 0.25], [0.75, 0.75], [0.25, 0.75], [0.75, 0.25], [0.25, 0.5], [0.75, 0.5]];
    const n = int(r, 1, 6);
    const shown = n % 2 ? [0, ...[1, 2, 3, 4].slice(0, n - 1)] : n === 6 ? [1, 2, 3, 4, 5, 6] : [1, 2, 3, 4].slice(0, n);
    out += `<g transform="rotate(${f1(rot)} ${f1(x + 21)} ${f1(y + 21)})"><rect x="${f1(x)}" y="${f1(y)}" width="42" height="42" rx="7" fill="${a.surface}" stroke="${a.ink}" stroke-width="1.6"/>${shown.map((p) => `<circle cx="${f1(x + pips[p]![0]! * 42)}" cy="${f1(y + pips[p]![1]! * 42)}" r="3.2" fill="${a.ink}"/>`).join("")}</g>`;
  }
  // cards fanned in the corner
  for (let c = 0; c < 3; c++) out += `<rect x="286" y="44" width="46" height="64" rx="5" fill="${c === 2 ? a.accent : a.surface}" stroke="${a.ink}" stroke-width="1.5" transform="rotate(${-16 + c * 16 + r() * 4} 309 108)"/>`;
  return out;
}

/** A sheet of a notebook with a route drawn across it. */
function route(r: Rng, a: Art): string {
  let out = `<rect width="400" height="150" fill="${a.surface}"/>`;
  for (let y = 24; y < 140; y += 22) out += `<path d="M24 ${y}H376" stroke="${a.line}" stroke-width="1"/>`;
  let x = 40;
  let y = 110 - r() * 30;
  let d = `M${f1(x)} ${f1(y)}`;
  const stops: [number, number][] = [[x, y]];
  while (x < 340) {
    const nx = x + 40 + r() * 50;
    const ny = Math.min(126, Math.max(28, y + (r() - 0.55) * 60));
    d += `Q${f1(x + (nx - x) / 2)} ${f1(Math.min(134, Math.max(20, y + (r() - 0.5) * 40)))} ${f1(nx)} ${f1(ny)}`;
    [x, y] = [nx, ny];
    stops.push([x, y]);
  }
  out += `<path d="${d}" stroke="${a.accent}" stroke-width="2.2" stroke-dasharray="1 7"/>`;
  stops.forEach(([sx, sy], i) => {
    out += i === stops.length - 1 ? `<path d="M${f1(sx - 6)} ${f1(sy - 6)}l12 12m0 -12l-12 12" stroke="${a.ink}" stroke-width="2.2"/>` : `<circle cx="${f1(sx)}" cy="${f1(sy)}" r="${i ? 3.5 : 5}" fill="${a.surface}" stroke="${a.ink}" stroke-width="1.8"/>`;
  });
  return out;
}

const HERO_ART: Record<Kind, ((r: Rng, a: Art) => string) | null> = { notes: route, tool: null, recipes: bowl, club: board, studio: pots, gallery: (r, a) => plot(r, a, 400, 170) };
/** The size of the first screen's drawing: a strip where the page is a column of text or a wall of sheets. */
const HERO_SIZE: Record<Kind, [number, number]> = { notes: [400, 150], tool: [400, 300], recipes: [400, 300], club: [400, 300], studio: [400, 300], gallery: [400, 170] };

/** The brand's mark: its first letter in a shape of the seed's. */
function mark(r: Rng, brand: string, p: Palette): string {
  const letter = esc((brand.replace(/[^\p{L}\p{N}]/gu, "")[0] ?? "•").toUpperCase());
  const shape = [`<rect x="2" y="2" width="28" height="28" rx="${int(r, 0, 9)}"`, `<circle cx="16" cy="16" r="14"`, `<path d="M16 1 31 16 16 31 1 16Z"`][int(r, 0, 2)]!;
  return `<svg viewBox="0 0 32 32" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">${shape} fill="${p.accent}"/><text x="16" y="21.5" text-anchor="middle" font-family="Georgia,serif" font-size="16" font-weight="700" fill="${p.on}">${letter}</text></svg>`;
}

/** The page for doc: the same bytes for the same document. */
export function renderSite(doc: SiteDoc): string {
  const r = rngOf("page:" + doc.seed);
  const c = namer(rngOf("names:" + doc.seed));
  const d = PALETTES[doc.kind][0]!;
  const p: Palette = {
    bg: hex(doc.theme.palette.bg, d.bg),
    surface: hex(doc.theme.palette.surface, d.surface),
    ink: hex(doc.theme.palette.ink, d.ink),
    muted: hex(doc.theme.palette.muted, d.muted),
    line: hex(doc.theme.palette.line, d.line),
    accent: hex(doc.theme.palette.accent, d.accent),
    on: hex(doc.theme.palette.on, d.on),
  };
  const art: Art = { ink: p.ink, accent: p.accent, surface: p.surface, line: p.line };
  const radius = Math.min(28, Math.max(0, Math.round(doc.theme.radius) || 0));
  const width = Math.min(1280, Math.max(600, Math.round(doc.theme.width) || 960));
  const heading = STACKS[doc.theme.heading] ?? STACKS.serif;
  const body = STACKS[doc.theme.body] ?? STACKS.sans;
  const mono = doc.theme.body === "mono";
  const unit = pick(r, [20, 22, 24, 26, 28]);
  const gap = pick(r, [56, 64, 72, 80, 88]);
  const centred = doc.kind === "gallery" ? r() < 0.5 : r() < 0.2;
  const ruled = r() < 0.6;
  const filled = r() < 0.5;
  const heroArt = HERO_ART[doc.kind];
  const split = heroArt !== null && doc.kind !== "gallery" && width >= 900;

  const sections = doc.blocks.filter((b) => b.t !== "hero");
  const anchors = new Map(sections.map((b, i) => [b.id, c("sec" + i)]));
  const nav = sections.filter((b) => b.title.trim() && b.title.length <= 26).slice(0, 4);
  const firstId = sections[0] ? anchors.get(sections[0].id)! : "";

  const blockHTML = (b: Block): string => {
    switch (b.t) {
      case "hero": {
        const drawing = heroArt ? `<div class="${c("art")}">${svg(heroArt(rngOf("hero:" + doc.seed), art), doc.tag, ...HERO_SIZE[doc.kind])}</div>` : "";
        const more = sections[0] ? `<p><a class="${c("more")}" href="#${firstId}">${esc(sections[0].title)} <span aria-hidden="true">↓</span></a></p>` : "";
        return `<section class="${c("hero")}${split ? " " + c("split") : ""}"><div><h1>${esc(b.title)}</h1><p class="${c("lead")}">${esc(b.text)}</p>${more}</div>${drawing}</section>`;
      }
      case "list": {
        const head = `<h2>${esc(b.title)}</h2>`;
        if (b.style === "steps") {
          return head + `<ol class="${c("steps")}">${b.items.map((it) => `<li><h3>${esc(it.title)}</h3><p>${esc(it.text)}</p></li>`).join("")}</ol>`;
        }
        if (b.style === "rows") {
          return head + `<ul class="${c("rows")}">${b.items.map((it) => `<li>${it.meta ? `<span class="${c("meta")}">${esc(it.meta)}</span>` : ""}<div><h3>${esc(it.title)}</h3><p>${esc(it.text)}</p></div></li>`).join("")}</ul>`;
        }
        return head + `<ul class="${c("cards")}">${b.items.map((it) => `<li><h3>${esc(it.title)}</h3>${it.meta ? `<span class="${c("meta")}">${esc(it.meta)}</span>` : ""}<p>${esc(it.text)}</p></li>`).join("")}</ul>`;
      }
      case "prose":
        return `<h2>${esc(b.title)}</h2><div class="${c("prose")}">${b.paras.map((t) => `<p>${esc(t)}</p>`).join("")}</div>`;
      case "table":
        return `<h2>${esc(b.title)}</h2><table class="${c("table")}"><tbody>${b.rows.map((row) => `<tr><th scope="row">${esc(row.name)}${row.note ? `<span>${esc(row.note)}</span>` : ""}</th><td>${esc(row.value)}</td></tr>`).join("")}</tbody></table>`;
      case "gallery": {
        const works = b.works.map((title, i) => {
          const wr = rngOf(`work:${doc.seed}:${i}:${title}`);
          const drawing = doc.kind === "studio" ? svg(`<rect width="240" height="240" fill="${p.surface}"/><path d="M20 206H220" stroke="${p.ink}" stroke-width="1.6"/>` + vessel(wr, art, 120, 206, 1.05, i % 3 === 0 ? p.accent : p.line), title, 240, 240) : svg(plot(wr, art, 320, 240), title, 320, 240);
          return `<figure>${drawing}<figcaption>${esc(title)}</figcaption></figure>`;
        });
        return `<h2>${esc(b.title)}</h2><div class="${c("works")}">${works.join("")}</div>`;
      }
      case "code":
        return `<h2>${esc(b.title)}</h2><pre class="${c("code")}"><code>${b.lines.map((l) => (l.startsWith("$ ") ? `<span>$</span> ${esc(l.slice(2))}` : esc(l))).join("\n")}</code></pre>`;
    }
  };

  const hero = doc.blocks.filter((b) => b.t === "hero").map(blockHTML).join("");
  const main = sections.map((b) => `<section class="${c("sec")}" id="${anchors.get(b.id)}">${blockHTML(b)}</section>`).join("");
  const logo = mark(rngOf("mark:" + doc.seed), doc.brand, p);
  const icon = "data:image/svg+xml," + encodeURIComponent(logo);

  // The rules, in an order of the seed's own: the cascade does not depend on it, the bytes do.
  const rules = shuffled(r, [
    `.${c("wrap")}{width:min(${width}px,100% - ${unit * 2}px);margin-inline:auto}`,
    `.${c("head")}{display:flex;flex-wrap:wrap;align-items:center;gap:${unit / 2}px ${unit}px;padding-block:${unit}px;${centred ? "flex-direction:column;text-align:center;" : "justify-content:space-between;"}border-bottom:1px solid var(--ln)}`,
    `.${c("brand")}{display:inline-flex;align-items:center;gap:10px;font-family:${heading};font-weight:700;font-size:${mono ? 17 : 19}px;color:var(--ink);text-decoration:none}`,
    `.${c("brand")} svg{width:30px;height:30px;flex:none}`,
    `.${c("nav")}{display:flex;flex-wrap:wrap;gap:4px ${unit}px;font-size:15px}`,
    `.${c("nav")} a{color:var(--mu);text-decoration:none;padding-block:8px}`,
    `.${c("nav")} a:hover,.${c("nav")} a:focus-visible{color:var(--ink);text-decoration:underline;text-underline-offset:4px}`,
    `.${c("hero")}{padding-block:${gap}px ${Math.round(gap * 0.8)}px}`,
    `.${c("split")}{display:grid;grid-template-columns:minmax(0,${pick(r, ["1.15fr", "1.25fr", "1fr"])}) minmax(0,1fr);gap:${unit * 2}px;align-items:center}`,
    `.${c("hero")} h1{font-family:${heading};font-size:clamp(30px,${mono ? "4.2vw" : "5vw"},${mono ? 42 : pick(r, [48, 52, 56])}px);line-height:1.12;letter-spacing:${mono ? "0" : "-.015em"};margin:0 0 ${unit}px;${split ? "" : "max-width:20ch;"}text-wrap:balance}`,
    `.${c("lead")}{font-size:${mono ? 16 : 19}px;line-height:1.6;color:var(--mu);margin:0 0 ${unit}px;max-width:56ch}`,
    `.${c("more")}{display:inline-block;padding:12px 0;color:var(--ac);font-weight:600;text-decoration:underline;text-underline-offset:5px;text-decoration-thickness:2px}`,
    `.${c("more")}:hover,.${c("more")}:focus-visible{text-decoration-thickness:3px}`,
    `.${c("art")} svg{display:block;width:100%;height:auto;border-radius:${radius}px;border:1px solid var(--ln)}`,
    `.${c("hero")}:not(.${c("split")}) .${c("art")}{margin-top:${unit * 1.5}px}`,
    `.${c("sec")}{padding-block:${Math.round(gap * 0.75)}px;${ruled ? "border-top:1px solid var(--ln)" : ""}}`,
    `.${c("sec")} h2{font-family:${heading};font-size:clamp(22px,3vw,${mono ? 26 : 30}px);line-height:1.2;margin:0 0 ${unit}px;text-wrap:balance}`,
    `h3{font-family:${heading};font-size:${mono ? 16 : 18}px;line-height:1.3;margin:0 0 6px}`,
    `.${c("cards")}{list-style:none;margin:0;padding:0;display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,${pick(r, [230, 250, 270])}px),1fr));gap:${unit}px}`,
    `.${c("cards")} li{padding:${unit}px;border-radius:${radius}px;${filled ? "background:var(--sf)" : "border:1px solid var(--ln)"}}`,
    `.${c("cards")} p,.${c("rows")} p,.${c("steps")} p{margin:0;color:var(--mu)}`,
    `.${c("meta")}{display:block;font-size:13px;color:var(--mu);font-variant-numeric:tabular-nums;margin-bottom:8px}`,
    `.${c("rows")}{list-style:none;margin:0;padding:0}`,
    `.${c("rows")} li{display:grid;grid-template-columns:${pick(r, [150, 160, 170])}px minmax(0,1fr);gap:${unit}px;padding-block:${unit}px;border-top:1px solid var(--ln)}`,
    `.${c("rows")} li:first-child{border-top:0;padding-top:0}`,
    `.${c("rows")} .${c("meta")}{margin:3px 0 0}`,
    `.${c("steps")}{margin:0;padding:0;list-style:none;counter-reset:${c("n")};display:grid;gap:${unit}px;max-width:68ch}`,
    `.${c("steps")} li{counter-increment:${c("n")};display:grid;grid-template-columns:40px minmax(0,1fr);column-gap:${unit / 2}px}`,
    `.${c("steps")} li::before{content:counter(${c("n")});grid-row:span 2;width:32px;height:32px;display:grid;place-items:center;border-radius:${Math.min(radius, 16)}px;background:var(--ac);color:var(--on);font-weight:700;font-size:15px;font-variant-numeric:tabular-nums}`,
    `.${c("prose")}{max-width:64ch}`,
    `.${c("prose")} p{margin:0 0 1em}`,
    `.${c("table")}{width:100%;border-collapse:collapse;max-width:${Math.min(width, 820)}px}`,
    `.${c("table")} th,.${c("table")} td{padding:${Math.round(unit * 0.6)}px 0;border-top:1px solid var(--ln);vertical-align:baseline}`,
    `.${c("table")} tr:first-child th,.${c("table")} tr:first-child td{border-top:0}`,
    `.${c("table")} th{text-align:left;font-weight:600;padding-right:${unit}px}`,
    `.${c("table")} th span{display:block;font-weight:400;color:var(--mu);font-size:.92em}`,
    `.${c("table")} td{text-align:right;white-space:nowrap;font-variant-numeric:tabular-nums}`,
    `.${c("works")}{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,${doc.kind === "studio" ? 170 : 240}px),1fr));gap:${unit}px}`,
    `.${c("works")} figure{margin:0}`,
    `.${c("works")} svg{display:block;width:100%;height:auto;border:1px solid var(--ln);border-radius:${radius}px}`,
    `.${c("works")} figcaption{margin-top:8px;font-size:14px;color:var(--mu)}`,
    `.${c("code")}{margin:0;padding:${unit}px;overflow-x:auto;background:var(--sf);border:1px solid var(--ln);border-radius:${radius}px;font:14px/1.7 ${STACKS.mono};tab-size:2}`,
    `.${c("code")} span{color:var(--mu);user-select:none}`,
    `.${c("foot")}{margin-top:${Math.round(gap / 2)}px;padding-block:${unit * 1.5}px;border-top:1px solid var(--ln);color:var(--mu);font-size:14px;display:flex;flex-wrap:wrap;gap:8px ${unit}px;justify-content:space-between}`,
  ]);
  const css =
    `:root{--bg:${p.bg};--sf:${p.surface};--ink:${p.ink};--mu:${p.muted};--ln:${p.line};--ac:${p.accent};--on:${p.on}}` +
    `*,*::before,*::after{box-sizing:border-box}` +
    `html{-webkit-text-size-adjust:100%;scroll-behavior:smooth}` +
    `body{margin:0;background:var(--bg);color:var(--ink);font:${mono ? 15 : doc.theme.body === "serif" ? 18 : 17}px/1.6 ${body}}` +
    `a{color:inherit}a:focus-visible{outline:2px solid var(--ac);outline-offset:3px}` +
    rules.join("") +
    `@media (max-width:${pick(r, [700, 720, 760])}px){.${c("split")}{grid-template-columns:1fr}.${c("rows")} li{grid-template-columns:1fr;gap:4px}.${c("table")} td{white-space:normal}}` +
    `@media (prefers-reduced-motion:reduce){html{scroll-behavior:auto}}`;

  const year = new Date().getFullYear();
  return (
    `<!doctype html><html lang="${doc.lang}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
    `<title>${esc(doc.brand)}${doc.tag ? " · " + esc(doc.tag) : ""}</title><meta name="description" content="${esc(doc.tag)}"><link rel="icon" href="${icon}"><style>${css}</style></head>` +
    `<body><div class="${c("wrap")}"><header class="${c("head")}"><a class="${c("brand")}" href="#">${logo}<span>${esc(doc.brand)}</span></a>` +
    (nav.length ? `<nav class="${c("nav")}" aria-label="${doc.lang === "ru" ? "Разделы" : "Sections"}">${nav.map((b) => `<a href="#${anchors.get(b.id)}">${esc(b.title)}</a>`).join("")}</nav>` : "") +
    `</header><main>${hero}${main}</main><footer class="${c("foot")}"><span>© ${year} ${esc(doc.brand)}</span><span>${esc(doc.foot)}</span></footer></div></body></html>`
  );
}
