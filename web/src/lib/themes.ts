/*
 * The colour themes (styles/themes.css), shared by the panel and the subscription page: which
 * ones exist and which are dark. Kept free of React so the subscription page's bundle takes
 * only this. public/theme-boot.js repeats the lists for the first paint of the panel.
 */

export const LIGHT_THEMES = ["mikan", "ocean", "sakura", "forest", "latte", "snow", "dawn", "dune"] as const;
export const DARK_THEMES = ["midnight", "graphite", "abyss", "ember", "plum", "moss", "terminal", "nord", "mocha", "tokyo", "dracula", "aurora", "cosmos"] as const;
export const THEMES = [...LIGHT_THEMES, ...DARK_THEMES] as const;
export type Theme = (typeof THEMES)[number];

export const isTheme = (v: string): v is Theme => (THEMES as readonly string[]).includes(v);
export const isDarkTheme = (theme: string) => (DARK_THEMES as readonly string[]).includes(theme);

/**
 * The subscription page's palettes that follow its light/dark mode (look.css has a light and
 * a dark side of each); every other theme is one or the other by itself.
 */
export const MODE_PALETTES = ["mikan", "ocean", "sakura", "forest"] as const;
export const followsMode = (palette: string) => (MODE_PALETTES as readonly string[]).includes(palette);
