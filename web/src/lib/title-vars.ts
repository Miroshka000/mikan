// The variables the title, the announcement and a host's name take (subs.titleValues fills them).
export const TITLE_VARS = ["brand", "name", "tariff", "date", "days", "used", "left", "total", "devices"] as const;

// The {words} of a text that are no variable: they stay as text in the apps, so this only
// warns (same case-sensitive match as the panel's substitution).
export function unknownVars(text: string): string[] {
  const found = new Set<string>();
  for (const m of text.matchAll(/\{([A-Za-z0-9_-]+)\}/g)) {
    if (!(TITLE_VARS as readonly string[]).includes(m[1] ?? "")) found.add(m[0]);
  }
  return [...found];
}
