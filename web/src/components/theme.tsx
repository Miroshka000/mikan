import { Check } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { t } from "../i18n";

/*
 * The panel's theme, kept in this browser. The palettes themselves are in styles/themes.css;
 * here is which ones exist, which are dark, and how a choice reaches <html>. The same list
 * is in public/theme-boot.js, which applies the theme before the first paint.
 */

const LIGHT = ["mikan", "ocean", "sakura", "forest", "latte", "snow", "dawn", "dune"] as const;
const DARK = ["midnight", "graphite", "abyss", "ember", "plum", "moss", "terminal", "nord", "mocha", "tokyo", "dracula", "aurora", "cosmos"] as const;
export const THEMES = [...LIGHT, ...DARK] as const;
export type Theme = (typeof THEMES)[number];
/** What the admin picks: a theme, or the device's light or dark (Mikan or Midnight). */
export type ThemeChoice = Theme | "system";

const KEY = "mikan-theme";

function valid(value: string | null): value is ThemeChoice {
  return value === "system" || (value !== null && (THEMES as readonly string[]).includes(value));
}

export const isDark = (theme: Theme) => (DARK as readonly string[]).includes(theme);

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

export function ThemeCard() {
  const [choice, set] = useState<ThemeChoice>(getTheme);
  const refs = useRef(new Map<ThemeChoice, HTMLButtonElement>());

  const names: Record<Theme, string> = {
    mikan: t("settings.themeMikan"),
    ocean: t("settings.themeOcean"),
    sakura: t("settings.themeSakura"),
    forest: t("settings.themeForest"),
    midnight: t("settings.themeMidnight"),
    graphite: t("settings.themeGraphite"),
    abyss: t("settings.themeAbyss"),
    ember: t("settings.themeEmber"),
    plum: t("settings.themePlum"),
    moss: t("settings.themeMoss"),
    terminal: t("settings.themeTerminal"),
    latte: t("settings.themeLatte"),
    snow: t("settings.themeSnow"),
    dawn: t("settings.themeDawn"),
    dune: t("settings.themeDune"),
    nord: t("settings.themeNord"),
    mocha: t("settings.themeMocha"),
    tokyo: t("settings.themeTokyo"),
    dracula: t("settings.themeDracula"),
    aurora: t("settings.themeAurora"),
    cosmos: t("settings.themeCosmos"),
  };
  const groups: { label: string; options: Option[] }[] = [
    { label: t("settings.themeLight"), options: LIGHT.map((id) => ({ id, label: names[id] })) },
    { label: t("settings.themeDark"), options: DARK.map((id) => ({ id, label: names[id] })) },
    { label: t("settings.themeAuto"), options: [{ id: "system", label: t("settings.themeSystem") }] },
  ];
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
    refs.current.get(to)?.focus();
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
    <section className="card glass reveal span-all" style={{ "--i": 4 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.theme")}</h2>
          <div className="card-sub">{t("settings.themeSub")}</div>
        </div>
      </div>
      <div role="radiogroup" aria-label={t("settings.theme")} className="flex flex-col gap-4">
        {groups.map((g) => (
          <div key={g.label} role="group" aria-label={g.label}>
            <div className="theme-group-label">{g.label}</div>
            <div className="theme-tiles">
              {g.options.map((o) => {
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
    </section>
  );
}

/** A small panel drawn from a theme's own tokens: its backdrop, sidebar, a card, the accent. */
function Mock({ theme }: { theme: Theme }) {
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
