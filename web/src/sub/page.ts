// What the admin made of the subscription page (internal/panel/subpage): the server puts it
// into the page's HTML as a JSON block, so the page knows its look before anything loads;
// the admin's preview builds the same shape from the editor's draft.
import type { CSSProperties } from "react";
import type { components } from "../api/schema";
import type { Platform } from "./apps";
import { followsMode, isDarkTheme, isTheme } from "../lib/themes";

export type PageConfig = components["schemas"]["Page"];
export type PageBlock = components["schemas"]["PageBlock"];
export type PageLook = components["schemas"]["PageLook"];
export type BlockType = PageBlock["type"];
export type DocItem = { id: number; title: string; emoji?: string; platform?: string };
export type DocView = DocItem & { body: string };

/** The page's data block (subpage.Public). */
export type PagePublic = {
  config: PageConfig;
  brand: string;
  /** The brand's colour when the page takes it; none keeps the palette's. */
  accent?: string;
  /** Image links relative to the subscription path. */
  logo?: string;
  background?: string;
  docs: DocItem[];
};

/** Blocks shown only in the Telegram Mini App: buying needs Telegram's sign-in. */
export const MINI_APP_ONLY: readonly BlockType[] = ["promo", "shop"];
export const CUSTOM_TYPES: readonly BlockType[] = ["text", "links"];
export const PLATFORMS: readonly Platform[] = ["ios", "android", "windows", "macos", "linux"];

const emptyApps = { order: [], hidden: [] };

/**
 * The page as it always was. The server sends its own (subpage.Default) with every page;
 * this copy is for a page served without one (the Vite dev server). Keep the two in step.
 */
export const DEFAULT_PAGE: PageConfig = {
  look: { palette: "mikan", mode: "light", accent: "theme", font: "default", radius: "medium", cards: "glass", background: { kind: "theme", from: "", to: "", angle: 160, dim: 30 } },
  brand: { logo: "letter", emoji: "", subtitle: "" },
  blocks: (
    [
      ["announce", false],
      ["status", true],
      ["promo", true],
      ["shop", true],
      ["traffic", true],
      ["devices", true],
      ["apps", true],
      ["guide", true],
      ["instructions", true],
      ["link", true],
      ["locations", false],
      ["telegram", true],
      ["support", true],
    ] as const
  ).map(([type, on]) => ({ id: type, type, on, title: "" })),
  apps: { platform: "auto", ios: emptyApps, android: emptyApps, windows: emptyApps, macos: emptyApps, linux: emptyApps, qr: true },
  og: { title: "", description: "", image: false },
  css: "",
};

/** The page's data from its HTML; the default page when it has none. */
export function readPage(): PagePublic {
  try {
    const raw = document.getElementById("mikan-page")?.textContent;
    if (raw) {
      const p = JSON.parse(raw) as PagePublic;
      if (p?.config?.blocks) return { ...p, docs: p.docs ?? [] };
    }
  } catch {
    // a page without its data shows as it always did
  }
  return { config: DEFAULT_PAGE, brand: "", docs: [] };
}

/** The scheme the page is shown in: the admin's, or the visitor's with "system". */
export function schemeOf(mode: PageLook["mode"], systemDark: boolean): "light" | "dark" {
  return mode === "system" ? (systemDark ? "dark" : "light") : mode;
}

const hex = (c: string) => [1, 3, 5].map((i) => parseInt(c.slice(i, i + 2), 16) / 255);

/** Text on a colour: dark on a light one, white on the rest (WCAG relative luminance). */
export function onColor(color: string): string {
  if (!/^#[0-9a-f]{6}$/i.test(color)) return "#ffffff";
  const [r, g, b] = hex(color).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)) as [number, number, number];
  const l = 0.2126 * r + 0.7152 * g + 0.0722 * b;
  // Contrast with white (1.05/(l+0.05)) against contrast with ink (#161a24, l≈0.011).
  return 1.05 / (l + 0.05) >= (l + 0.05) / 0.061 ? "#ffffff" : "#161a24";
}

/**
 * The attributes that put a look on an element with the class sub-look (look.css): the
 * page's <html>, or the admin's preview frame.
 */
export function lookAttrs(look: PageLook, scheme: "light" | "dark", accent?: string): { data: Record<string, string>; style: CSSProperties } {
  // A panel theme other than the four that follow the mode is light or dark by itself, and
  // brings all of its tokens (styles/themes.css) through data-theme and data-tone.
  const own = !followsMode(look.palette) && isTheme(look.palette);
  if (own) scheme = isDarkTheme(look.palette) ? "dark" : "light";
  const data: Record<string, string> = { palette: look.palette, scheme, font: look.font, radius: look.radius, cards: look.cards };
  if (own) {
    data.theme = look.palette;
    data.tone = scheme;
  }
  const style: Record<string, string> = {};
  if (accent) {
    data.accent = "own";
    style["--accent"] = accent;
    style["--on-accent"] = onColor(accent);
  }
  return { data, style: style as CSSProperties };
}

/** Puts a look on el (the page's <html>, the preview's), replacing the one it had. */
export function paintLook(el: HTMLElement, look: PageLook, scheme: "light" | "dark", accent?: string) {
  const { data, style } = lookAttrs(look, scheme, accent);
  el.classList.add("sub-look");
  for (const k of ["palette", "scheme", "font", "radius", "cards", "accent", "theme", "tone"]) {
    if (data[k]) el.dataset[k] = data[k];
    else delete el.dataset[k];
  }
  for (const k of ["--accent", "--on-accent"]) {
    const v = (style as Record<string, string>)[k];
    if (v) el.style.setProperty(k, v);
    else el.style.removeProperty(k);
  }
}

/** The backdrop's own CSS when the admin chose one; null keeps the theme's. */
export function backdropStyle(look: PageLook, image?: string): CSSProperties | null {
  const b = look.background;
  switch (b.kind) {
    case "solid":
      return b.from ? { background: b.from } : null;
    case "gradient":
      return b.from && b.to ? { background: `linear-gradient(${b.angle}deg, ${b.from}, ${b.to})` } : null;
    case "image":
      return image ? { backgroundImage: `url("${encodeURI(image)}")`, backgroundSize: "cover", backgroundPosition: "center" } : null;
    default:
      return null;
  }
}

/** A platform's apps of the catalogue as the admin ordered them, hidden ones left out. */
export function orderApps<T extends { name: string }>(apps: T[], list: { order: string[]; hidden: string[] } | undefined): T[] {
  if (!list) return apps;
  const pos = (name: string) => {
    const i = list.order.indexOf(name);
    return i < 0 ? list.order.length + apps.findIndex((a) => a.name === name) : i;
  };
  return apps.filter((a) => !list.hidden.includes(a.name)).sort((a, b) => pos(a.name) - pos(b.name));
}

/** The title a block shows: the admin's own, or its usual one. */
export function blockTitle(block: PageBlock, usual: string): string {
  return block.title.trim() || usual;
}

/** A block's own heading, "" when it has none (or the page has no such block). */
export function titleOf(config: PageConfig, type: BlockType): string {
  return config.blocks.find((b) => b.type === type)?.title.trim() ?? "";
}

/**
 * The admin's CSS as the server keeps it (subpage.CleanCSS): no "<", no @import, no
 * at-rule spelled with an escape. The preview applies the draft the same way.
 */
export function cleanCSS(css: string): string {
  // As the server's subpage.CleanCSS: no escapes, no @import, no url() of another site.
  let out = css.replaceAll("<", "").replaceAll("\0", "").replaceAll("\\", "");
  for (;;) {
    const next = out.replace(/@import/gi, "").replace(/(url|image-set|image)\(\s*['"]?\s*(https?:|\/\/)[^)]*\)/gi, "none");
    if (next === out) break;
    out = next;
  }
  return out.trim();
}

/** An emoji as an icon: an SVG of one letter, a data: link the page's CSP allows. */
export function emojiIcon(emoji: string): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><text x="32" y="54" font-size="52" text-anchor="middle">${emoji.replace(/[<>&"']/g, "")}</text></svg>`;
  return "data:image/svg+xml," + encodeURIComponent(svg);
}

/**
 * Puts the page's look on its <html> (before the first paint: no flash of the default
 * look), its icon on the tab and its backdrop colour on the browser's bar. "system"
 * follows the visitor's scheme as it changes.
 */
export function applyLook(page: PagePublic, asset: (path?: string) => string | undefined) {
  const root = document.documentElement;
  const look = page.config.look;
  const dark = window.matchMedia?.("(prefers-color-scheme: dark)");
  const paint = () => {
    paintLook(root, look, schemeOf(look.mode, !!dark?.matches), page.accent);
    const bar = look.background.kind === "solid" || look.background.kind === "gradient" ? look.background.from : getComputedStyle(root).getPropertyValue("--bg").trim();
    if (bar) document.querySelector('meta[name="theme-color"]')?.setAttribute("content", bar);
  };
  paint();
  if (look.mode === "system") dark?.addEventListener?.("change", paint);

  const brand = page.config.brand;
  const icon = brand.logo === "image" ? asset(page.logo) : brand.logo === "emoji" && brand.emoji ? emojiIcon(brand.emoji) : undefined;
  if (icon) {
    for (const link of document.querySelectorAll<HTMLLinkElement>('link[rel="icon"], link[rel="apple-touch-icon"]')) {
      if (link.rel === "apple-touch-icon" && brand.logo !== "image") continue;
      link.removeAttribute("type");
      link.href = icon;
    }
  }
}
