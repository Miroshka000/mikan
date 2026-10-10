// Checks that every panel theme (src/styles/themes.css) keeps its text readable: WCAG AA,
// 4.5:1 for text, 3:1 for the faint labels and the accent's marks. Runs before the build.
// The tokens resolve the way the page does: the light base, then the dark base for a dark
// theme, then the theme's own block.
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("../src/styles/themes.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

function block(selector) {
  const at = css.indexOf(selector + " {");
  if (at < 0) return {};
  const body = css.slice(css.indexOf("{", at) + 1, css.indexOf("}", at));
  const out = {};
  for (const m of body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}

const light = block(":root, [data-tone=\"light\"]");
const dark = block("[data-tone=\"dark\"]");
const themes = [...css.matchAll(/^\[data-theme="([\w-]+)"\] \{/gm)].map((m) => m[1]);
const darkThemes = new Set(["midnight", "graphite", "abyss", "ember", "plum", "moss", "terminal", "nord", "mocha", "tokyo", "dracula", "aurora", "cosmos"]);

function hex(tokens, name, seen = new Set()) {
  const v = tokens[name];
  if (v === undefined) throw new Error(`${name} is not set`);
  const ref = /^var\((--[\w-]+)\)$/.exec(v);
  if (ref) {
    if (seen.has(ref[1])) throw new Error(`${name} refers to itself`);
    seen.add(ref[1]);
    return hex(tokens, ref[1], seen);
  }
  if (/^#[0-9a-f]{3}$/i.test(v)) return "#" + [...v.slice(1)].map((c) => c + c).join("");
  if (!/^#[0-9a-f]{6}$/i.test(v)) throw new Error(`${name}: ${v} is not a hex colour`);
  return v;
}

function luminance(h) {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(h.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function ratio(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

// [text, background, minimum]
const pairs = [
  ["--ink-900", "--bg", 7],
  ["--ink-900", "--surface-solid", 7],
  ["--ink-700", "--surface-solid", 4.5],
  ["--ink-500", "--surface-solid", 4.5],
  ["--ink-500", "--bg", 4.5],
  ["--ink-400", "--surface-solid", 3],
  ["--on-fill", "--fill", 4.5],
  ["--mikan-700", "--surface-solid", 4.5],
  // The focus ring and the active icons; --mikan-500 fills bars that always have a number beside them.
  ["--mikan-600", "--surface-solid", 3],
  ["--leaf-700", "--leaf-50", 4.5],
  ["--honey-600", "--honey-50", 4.5],
  ["--berry-600", "--berry-50", 4.5],
  ["--lagoon-600", "--lagoon-50", 4.5],
  ["--berry-600", "--surface-solid", 4.5],
  ["--code-fg", "--code-bg", 7],
];

let failed = 0;
const rows = [];
for (const name of ["mikan", ...themes]) {
  const tokens = { ...light, ...(darkThemes.has(name) ? dark : {}), ...block(`[data-theme="${name}"]`) };
  let worst = Infinity;
  for (const [fg, bg, min] of pairs) {
    const r = ratio(hex(tokens, fg), hex(tokens, bg));
    worst = Math.min(worst, r / min);
    if (r < min) {
      failed++;
      console.error(`contrast: ${name}: ${fg} on ${bg} is ${r.toFixed(2)}:1, needs ${min}:1`);
    }
  }
  rows.push(`${name} ${(worst * 100).toFixed(0)}%`);
}
if (failed) {
  console.error(`contrast: ${failed} pair(s) below WCAG AA`);
  process.exit(1);
}
console.log(`contrast: AA in every theme (closest to the limit: ${rows.join(", ")})`);
