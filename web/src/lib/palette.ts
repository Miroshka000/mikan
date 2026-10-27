// The command palette's rules that have no screen: the key that opens it, how a typed text
// matches a line, and the id of a user typed as a number.

/** The key of the palette as the event reads it. */
type Keys = Pick<KeyboardEvent, "key" | "code" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey">;

/**
 * Ctrl+K, and ⌘K on a Mac (where Ctrl+K is the text fields' "cut to the end of the line").
 * The physical key counts too, so a Russian layout (where the key types "л") opens it as well.
 */
export function isPaletteKey(e: Keys, mac: boolean): boolean {
  if (e.altKey || e.shiftKey) return false;
  if (mac ? !e.metaKey || e.ctrlKey : !e.ctrlKey || e.metaKey) return false;
  return e.code === "KeyK" || e.key.toLowerCase() === "k";
}

/** The browser runs on a Mac (an iPad with a keyboard included). */
export function onMac(): boolean {
  if (typeof navigator === "undefined") return false;
  const platform = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform || navigator.platform || "";
  return /mac|iphone|ipad|ipod/i.test(platform);
}

/** The words of a search, in lower case; "ё" is "е", as people type it. */
function words(q: string): string[] {
  return q.toLowerCase().replaceAll("ё", "е").split(/\s+/).filter(Boolean);
}

/** Every word of the search is somewhere in the text, in any order. An empty search matches all. */
export function matches(text: string, q: string): boolean {
  const hay = text.toLowerCase().replaceAll("ё", "е");
  return words(q).every((w) => hay.includes(w));
}

/** The user's id when the search is one ("12" or "#12"): the list's search does not look at ids. */
export function userIdOf(q: string): number | undefined {
  const m = /^#?(\d{1,15})$/.exec(q.trim());
  const id = m ? Number(m[1]) : 0;
  return Number.isSafeInteger(id) && id >= 1 ? id : undefined;
}

/** The line the arrows move to among n lines: past the last one is the first again, and back. */
export function step(at: number, n: number, by: 1 | -1): number {
  if (n <= 0) return 0;
  return (((at + by) % n) + n) % n;
}
