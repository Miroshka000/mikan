import interCyr from "@fontsource-variable/inter/files/inter-cyrillic-wght-normal.woff2?url";
import interLat from "@fontsource-variable/inter/files/inter-latin-wght-normal.woff2?url";
import jetCyr from "@fontsource-variable/jetbrains-mono/files/jetbrains-mono-cyrillic-wght-normal.woff2?url";
import jetLat from "@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2?url";
import loraCyr from "@fontsource-variable/lora/files/lora-cyrillic-wght-normal.woff2?url";
import loraLat from "@fontsource-variable/lora/files/lora-latin-wght-normal.woff2?url";
import manropeCyr from "@fontsource-variable/manrope/files/manrope-cyrillic-wght-normal.woff2?url";
import manropeLat from "@fontsource-variable/manrope/files/manrope-latin-wght-normal.woff2?url";
import playfairCyr from "@fontsource-variable/playfair-display/files/playfair-display-cyrillic-wght-normal.woff2?url";
import playfairLat from "@fontsource-variable/playfair-display/files/playfair-display-latin-wght-normal.woff2?url";
import rubikCyr from "@fontsource-variable/rubik/files/rubik-cyrillic-wght-normal.woff2?url";
import rubikLat from "@fontsource-variable/rubik/files/rubik-latin-wght-normal.woff2?url";
import serifCyr from "@fontsource-variable/source-serif-4/files/source-serif-4-cyrillic-wght-normal.woff2?url";
import serifLat from "@fontsource-variable/source-serif-4/files/source-serif-4-latin-wght-normal.woff2?url";
import type { FontId, Lang } from "./model";

const LATIN = "U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD";
const CYRILLIC = "U+0301,U+0400-045F,U+0490-0491,U+04B0-04B1,U+2116";

/** A font file of a family: where the panel keeps it, and the letters it holds. */
export type FontFile = { script: "latin" | "cyrillic"; url: string; range: string };

/**
 * A typeface a site can be set in. The ones with files are open fonts the panel carries: a
 * site serves them from its own address, as any site that hosts its fonts does. The system
 * ones have no file: the visitor's own fonts.
 */
export class FontFamily {
  constructor(
    readonly id: FontId,
    readonly name: string,
    readonly fallback: string,
    readonly weights: string,
    private readonly latin?: string,
    private readonly cyrillic?: string,
  ) {}

  /** The value of font-family: the family, then fonts every system has. */
  get stack(): string {
    return this.latin ? `"${this.name}",${this.fallback}` : this.fallback;
  }

  /** The files a site in lang needs of this family: Cyrillic only where the words are. */
  files(lang: Lang): FontFile[] {
    const out: FontFile[] = [];
    if (this.latin) out.push({ script: "latin", url: this.latin, range: LATIN });
    if (this.cyrillic && lang === "ru") out.push({ script: "cyrillic", url: this.cyrillic, range: CYRILLIC });
    return out;
  }
}

const SANS = 'system-ui,-apple-system,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif';
const SERIF = 'Georgia,"Times New Roman",serif';
const MONO = 'ui-monospace,"SF Mono","Cascadia Mono",Consolas,Menlo,monospace';

export const FONTS: Record<FontId, FontFamily> = {
  inter: new FontFamily("inter", "Inter", SANS, "100 900", interLat, interCyr),
  manrope: new FontFamily("manrope", "Manrope", SANS, "200 800", manropeLat, manropeCyr),
  rubik: new FontFamily("rubik", "Rubik", SANS, "300 900", rubikLat, rubikCyr),
  lora: new FontFamily("lora", "Lora", SERIF, "400 700", loraLat, loraCyr),
  playfair: new FontFamily("playfair", "Playfair Display", SERIF, "400 900", playfairLat, playfairCyr),
  "source-serif": new FontFamily("source-serif", "Source Serif 4", SERIF, "200 900", serifLat, serifCyr),
  jetbrains: new FontFamily("jetbrains", "JetBrains Mono", MONO, "100 800", jetLat, jetCyr),
  "system-sans": new FontFamily("system-sans", "", SANS, ""),
  "system-serif": new FontFamily("system-serif", "", SERIF, ""),
};

export const FONT_IDS = Object.keys(FONTS) as FontId[];
export const fontOf = (id: FontId): FontFamily => FONTS[id] ?? FONTS["system-sans"];
