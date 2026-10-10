import { Check, ChevronDown } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { t } from "../i18n";
import { DARK_THEMES, isDarkTheme, LIGHT_THEMES, THEMES, type Theme } from "../lib/themes";

/*
 * The panel's theme, kept in this browser. The palettes themselves are in styles/themes.css;
 * here is which ones exist, which are dark, and how a choice reaches <html>. The same list
 * is in public/theme-boot.js, which applies the theme before the first paint.
 */

const LIGHT = LIGHT_THEMES;
const DARK = DARK_THEMES;
export { THEMES, type Theme };
/** What the admin picks: a theme, or the device's light or dark (Mikan or Midnight). */
export type ThemeChoice = Theme | "system";

const KEY = "mikan-theme";

function valid(value: string | null): value is ThemeChoice {
  return value === "system" || (value !== null && (THEMES as readonly string[]).includes(value));
}

export const isDark = (theme: Theme) => isDarkTheme(theme);

export function getTheme(): ThemeChoice {
  if (typeof window === "undefined") return "mikan";
  try {
    const value = window.localStorage.getItem(KEY);
    return valid(value) ? value : "mikan";
  } catch {
    return "mikan";
  }
}

const darkQuery = () => window.matchMedia?.("(prefers-color-scheme: dark)");

function resolve(choice: ThemeChoice): Theme {
  if (choice !== "system") return choice;
  return darkQuery()?.matches ? "midnight" : "mikan";
}

function apply(theme: Theme) {
  const root = document.documentElement;
  root.dataset.theme = theme;
  root.dataset.tone = isDark(theme) ? "dark" : "light";
  // The browser's own bars (Android, Safari) take the page's colour.
  const bg = getComputedStyle(root).getPropertyValue("--bg").trim();
  document.querySelector('meta[name="theme-color"]')?.setAttribute("content", bg || "#fbf1e8");
}

let following: MediaQueryList | undefined;
const onSchemeChange = () => {
  if (getTheme() === "system") apply(resolve("system"));
};

/** Applies a choice to the page and keeps it; "system" also follows the device from now on. */
export function setTheme(choice: ThemeChoice, opts: { animate?: boolean; keep?: boolean } = {}) {
  const next: ThemeChoice = valid(choice) ? choice : "mikan";
  // keep: false only shows it. Another tab's choice is applied without writing it back, so
  // two tabs of different versions never overwrite each other's theme.
  if (opts.keep !== false) {
    try {
      window.localStorage.setItem(KEY, next);
    } catch {
      // Storage can be unavailable in private/restricted browser contexts.
    }
  }
  const mq = darkQuery();
  if (next === "system" && mq && following !== mq) {
    mq.addEventListener("change", onSchemeChange);
    following = mq;
  }
  const theme = resolve(next);
  const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
  // A cross-fade of the whole page where the browser can draw one, an instant switch elsewhere.
  if (opts.animate && !reduce && "startViewTransition" in document) {
    (document as Document & { startViewTransition: (cb: () => void) => unknown }).startViewTransition(() => apply(theme));
  } else {
    apply(theme);
  }
}

type Option = { id: ThemeChoice; label: string };

/** A tile's least width and the gap between tiles: .theme-tiles in app.css. */
const TILE_MIN = 148;
const TILE_GAP = 12;

/**
 * How many tiles fit in one row of el's width, followed as it changes: a folded group
 * shows one full row, whether the card is a column of the settings or the whole width.
 */
export function useTilesPerRow(): [React.RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [n, setN] = useState(4);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = (w: number) => setN(Math.max(1, Math.floor((w + TILE_GAP) / (TILE_MIN + TILE_GAP))));
    measure(el.getBoundingClientRect().width);
    const ro = new ResizeObserver(([e]) => e && measure(e.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, n];
}

/**
 * A group's tiles while the list is folded: one row of n, the chosen one taking the last
 * place when it is further down, so a row never spills into a second.
 */
export function foldedList<T extends { id: string }>(list: readonly T[], chosen: string, open: boolean, n: number): T[] {
  if (open || list.length <= n) return [...list];
  const head = list.slice(0, n);
  const pick = list.find((o) => o.id === chosen);
  return pick && !head.includes(pick) ? [...head.slice(0, n - 1), pick] : head;
}

/** The button under a folded list of themes: all of them, or back to a few. */
export function ThemesToggle({ open, total, onToggle }: { open: boolean; total: number; onToggle: () => void }) {
  return (
    <button type="button" className="theme-more" aria-expanded={open} onClick={onToggle}>
      <span>{open ? t("settings.themesLess") : t("settings.themesAll", { n: total })}</span>
      <ChevronDown size={16} aria-hidden />
    </button>
  );
}

export function ThemeCard() {
  const [choice, set] = useState<ThemeChoice>(getTheme);
  const [open, setOpen] = useState(false);
  const [rows, perRow] = useTilesPerRow();
  const refs = useRef(new Map<ThemeChoice, HTMLButtonElement>());

  const names = themeNames();
  const groups: { label: string; options: Option[] }[] = [
    { label: t("settings.themeLight"), options: LIGHT.map((id) => ({ id, label: names[id] })) },
    { label: t("settings.themeDark"), options: DARK.map((id) => ({ id, label: names[id] })) },
    // Folded, "as the system" waits behind "All themes" unless it is the choice.
    ...(open || choice === "system" ? [{ label: t("settings.themeAuto"), options: [{ id: "system" as const, label: t("settings.themeSystem") }] }] : []),
  ];
  const folds = THEMES.length + 1 > groups.reduce((sum, g) => sum + Math.min(g.options.length, perRow), 0);
  const order = groups.flatMap((g) => g.options.map((o) => o.id));

  const pick = (id: ThemeChoice) => {
    if (id === choice) return;
    set(id);
    setTheme(id, { animate: true });
  };

  // Radio group keys: the arrows move the choice and the focus, Home and End jump to the ends.
  const onKey = (e: React.KeyboardEvent, id: ThemeChoice) => {
    const i = order.indexOf(id);
    const to =
      e.key === "ArrowRight" || e.key === "ArrowDown" ? order[(i + 1) % order.length]
      : e.key === "ArrowLeft" || e.key === "ArrowUp" ? order[(i - 1 + order.length) % order.length]
      : e.key === "Home" ? order[0]
      : e.key === "End" ? order[order.length - 1]
      : undefined;
    if (!to) return;
    e.preventDefault();
    pick(to);
    // The next theme may be folded away: unfold, then focus it once it is there.
    setOpen(true);
    requestAnimationFrame(() => refs.current.get(to)?.focus());
  };

  // Another tab may change the theme: follow it here too.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (e.key !== KEY) return;
      const next = getTheme();
      set(next);
      setTheme(next, { keep: false });
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  return (
    <section className="card glass reveal" style={{ "--i": 4 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.theme")}</h2>
          <div className="card-sub">{t("settings.themeSub")}</div>
        </div>
      </div>
      <div ref={rows} role="radiogroup" aria-label={t("settings.theme")} className="flex flex-col gap-4">
        {groups.map((g) => (
          <div key={g.label} role="group" aria-label={g.label}>
            <div className="theme-group-label">{g.label}</div>
            <div className="theme-tiles">
              {foldedList(g.options, choice, open, perRow).map((o) => {
                const checked = choice === o.id;
                return (
                  <button
                    key={o.id}
                    ref={(el) => {
                      if (el) refs.current.set(o.id, el);
                      else refs.current.delete(o.id);
                    }}
                    type="button"
                    role="radio"
                    aria-checked={checked}
                    aria-label={o.label}
                    tabIndex={checked ? 0 : -1}
                    className="theme-tile"
                    onClick={() => pick(o.id)}
                    onKeyDown={(e) => onKey(e, o.id)}
                  >
                    {o.id === "system" ? (
                      <span className="theme-mock-split" aria-hidden>
                        <Mock theme="mikan" />
                        <Mock theme="midnight" />
                      </span>
                    ) : (
                      <Mock theme={o.id} />
                    )}
                    <span className="theme-tile-name">
                      <span>{o.label}</span>
                      {checked ? <Check size={14} aria-hidden /> : null}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>
        ))}
      </div>
      {folds || open ? <ThemesToggle open={open} total={THEMES.length} onToggle={() => setOpen((v) => !v)} /> : null}
    </section>
  );
}

/** Every theme's name, in the admin's language. */
export function themeNames(): Record<Theme, string> {
  return {
    mikan: t("settings.themeMikan"),
    ocean: t("settings.themeOcean"),
    sakura: t("settings.themeSakura"),
    forest: t("settings.themeForest"),
    latte: t("settings.themeLatte"),
    snow: t("settings.themeSnow"),
    dawn: t("settings.themeDawn"),
    dune: t("settings.themeDune"),
    midnight: t("settings.themeMidnight"),
    graphite: t("settings.themeGraphite"),
    abyss: t("settings.themeAbyss"),
    ember: t("settings.themeEmber"),
    plum: t("settings.themePlum"),
    moss: t("settings.themeMoss"),
    terminal: t("settings.themeTerminal"),
    nord: t("settings.themeNord"),
    mocha: t("settings.themeMocha"),
    tokyo: t("settings.themeTokyo"),
    dracula: t("settings.themeDracula"),
    aurora: t("settings.themeAurora"),
    cosmos: t("settings.themeCosmos"),
  };
}

/** A small panel drawn from a theme's own tokens: its backdrop, sidebar, a card, the accent. */
export function Mock({ theme }: { theme: Theme }) {
  return (
    <span className="theme-mock" data-theme={theme} data-tone={isDark(theme) ? "dark" : "light"} aria-hidden>
      <span className="tm-side">
        <i className="on" />
        <i />
        <i />
      </span>
      <span className="tm-card">
        <i className="tm-title" />
        <i className="tm-line" />
        <i className="tm-bar">
          <b />
        </i>
        <span className="tm-foot">
          <i className="tm-btn" />
          <i className="tm-dot" />
        </span>
      </span>
    </span>
  );
}
