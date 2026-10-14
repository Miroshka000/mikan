import { getLocale } from "../i18n";

/** A text an addon gives per language, in the panel's. */
export function localized(m: Record<string, string> | undefined, fallback: string): string {
  if (!m) return fallback;
  return m[getLocale()] || m.en || Object.values(m)[0] || fallback;
}
