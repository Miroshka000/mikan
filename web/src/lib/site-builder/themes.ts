import type { FontId, Kind, Layout, Palette, Theme } from "./model";
import type { Rng } from "./rng";

/** The colours of each kind of site, taken from what the site is about. */
const PALETTES: Record<Kind, Palette[]> = {
  notes: [
    { bg: "#fbf8f1", surface: "#f3eee2", ink: "#22201b", muted: "#6a6457", line: "#ddd5c3", accent: "#9a3b2a", on: "#ffffff" },
    { bg: "#f7f8f6", surface: "#eceee9", ink: "#1c2420", muted: "#5c675f", line: "#d3d8d0", accent: "#1f5a8a", on: "#ffffff" },
    { bg: "#ffffff", surface: "#f4f4f1", ink: "#1a1a1a", muted: "#666666", line: "#e2e2dc", accent: "#2f5d3a", on: "#ffffff" },
  ],
  tool: [
    { bg: "#101418", surface: "#171c22", ink: "#e3e8ee", muted: "#94a0ad", line: "#2a323b", accent: "#7bd389", on: "#0c1410" },
    { bg: "#fbfbf9", surface: "#f0f0eb", ink: "#1b1d1f", muted: "#5d6368", line: "#dcdcd4", accent: "#a84a06", on: "#ffffff" },
    { bg: "#ffffff", surface: "#f3f5f8", ink: "#16202c", muted: "#5a6675", line: "#dde2e9", accent: "#1d5fd0", on: "#ffffff" },
  ],
  recipes: [
    { bg: "#fdf6ec", surface: "#f6ead8", ink: "#2b1d14", muted: "#75604f", line: "#e6d5bd", accent: "#b8401f", on: "#ffffff" },
    { bg: "#f5f4ea", surface: "#e9e8d8", ink: "#232a1b", muted: "#5f6851", line: "#d5d6bf", accent: "#4f6a25", on: "#ffffff" },
    { bg: "#ffffff", surface: "#f8f1ea", ink: "#2a1a1a", muted: "#765a55", line: "#ecdccd", accent: "#a3352f", on: "#ffffff" },
  ],
  club: [
    { bg: "#f3f0e6", surface: "#e7e2d2", ink: "#17241d", muted: "#55635a", line: "#cfc9b5", accent: "#1d6b4a", on: "#ffffff" },
    { bg: "#f1f3f8", surface: "#e2e6f0", ink: "#141c33", muted: "#545d78", line: "#c9cfdf", accent: "#b93c0b", on: "#ffffff" },
    { bg: "#ffffff", surface: "#f2f2ee", ink: "#1c1a14", muted: "#66614f", line: "#deddd2", accent: "#1e4e79", on: "#ffffff" },
  ],
  studio: [
    { bg: "#f4eee6", surface: "#e9dfd2", ink: "#2a221d", muted: "#6f6258", line: "#d8cbbb", accent: "#a04627", on: "#ffffff" },
    { bg: "#eeece7", surface: "#e1ded6", ink: "#23262a", muted: "#5f646b", line: "#cfcbc1", accent: "#3d5a66", on: "#ffffff" },
    { bg: "#ffffff", surface: "#f5f1ea", ink: "#2b2118", muted: "#73624f", line: "#e3d8c6", accent: "#7a5a1e", on: "#ffffff" },
  ],
  gallery: [
    { bg: "#fafafa", surface: "#f0f0f0", ink: "#111111", muted: "#5f5f5f", line: "#d9d9d9", accent: "#111111", on: "#ffffff" },
    { bg: "#0e0e0e", surface: "#171717", ink: "#ededed", muted: "#9a9a9a", line: "#2b2b2b", accent: "#ededed", on: "#0e0e0e" },
    { bg: "#f2efe9", surface: "#e6e2d9", ink: "#1a1a1a", muted: "#63605a", line: "#d2cdc1", accent: "#c2301a", on: "#ffffff" },
  ],
};

/** The type, the corners, the width and the build a kind of site may have: one of each is drawn. */
const LOOKS: Record<Kind, { fonts: [FontId, FontId][]; radius: number[]; width: number[]; header: Layout["header"][]; sections: Layout["sections"][]; hero: Layout["hero"][] }> = {
  notes: { fonts: [["lora", "lora"], ["source-serif", "source-serif"], ["playfair", "inter"]], radius: [0, 2], width: [720, 780], header: ["bar", "centered"], sections: ["stack"], hero: ["plain"] },
  tool: { fonts: [["jetbrains", "inter"], ["jetbrains", "jetbrains"], ["inter", "inter"]], radius: [0, 4, 6], width: [880, 960], header: ["bar"], sections: ["stack", "aside"], hero: ["plain"] },
  recipes: { fonts: [["playfair", "inter"], ["lora", "manrope"], ["rubik", "rubik"]], radius: [6, 12], width: [1000, 1080], header: ["bar", "centered"], sections: ["stack", "aside"], hero: ["plain", "band"] },
  club: { fonts: [["rubik", "rubik"], ["manrope", "manrope"], ["inter", "inter"]], radius: [4, 10], width: [1000, 1080], header: ["bar"], sections: ["stack", "aside"], hero: ["band", "plain"] },
  studio: { fonts: [["playfair", "manrope"], ["source-serif", "inter"], ["lora", "lora"]], radius: [0, 4], width: [1040, 1120], header: ["bar", "centered"], sections: ["aside", "stack"], hero: ["plain", "band"] },
  gallery: { fonts: [["inter", "inter"], ["jetbrains", "inter"], ["manrope", "manrope"]], radius: [0], width: [1120, 1200], header: ["bar", "centered"], sections: ["stack"], hero: ["plain"] },
};

export const palettesOf = (kind: Kind): Palette[] => PALETTES[kind];

/** A theme for a site of kind, every choice drawn by rng. */
export function randomTheme(kind: Kind, rng: Rng): Theme {
  const look = LOOKS[kind];
  const [heading, body] = rng.pick(look.fonts);
  return {
    palette: { ...rng.pick(PALETTES[kind]) },
    heading,
    body,
    radius: rng.pick(look.radius),
    width: rng.pick(look.width),
    layout: { header: rng.pick(look.header), sections: rng.pick(look.sections), hero: rng.pick(look.hero) },
  };
}
