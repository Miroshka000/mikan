/*
 * How tight the panel is drawn, kept in this browser like the theme: "comfortable" (the
 * default) or "compact", which takes the room out of the tables, cards, forms and buttons
 * so more fits on a screen. Only a mouse gets it (styles/app.css: pointer: fine): a finger
 * keeps the full targets. public/theme-boot.js applies it before the first paint.
 */

export type Density = "comfortable" | "compact";

export const DENSITY_KEY = "mikan-density";

export function getDensity(): Density {
  if (typeof window === "undefined") return "comfortable";
  try {
    return window.localStorage.getItem(DENSITY_KEY) === "compact" ? "compact" : "comfortable";
  } catch {
    return "comfortable";
  }
}

/** Applies a density to the page and, unless keep is false, keeps it. */
export function setDensity(d: Density, opts: { keep?: boolean } = {}) {
  if (opts.keep !== false) {
    try {
      window.localStorage.setItem(DENSITY_KEY, d);
    } catch {
      // Storage can be unavailable in private/restricted browser contexts.
    }
  }
  if (d === "compact") document.documentElement.dataset.density = "compact";
  else delete document.documentElement.dataset.density;
}
