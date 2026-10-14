import { useCallback, useSyncExternalStore } from "react";

/** Whether a CSS media query matches now; follows it as the window changes. */
export function useMediaQuery(query: string): boolean {
  // Stable while the query is: a new function each render would drop the listener and add
  // it again (with a fresh matchMedia) on every render of every caller.
  const subscribe = useCallback(
    (notify: () => void) => {
      const m = window.matchMedia(query);
      m.addEventListener("change", notify);
      return () => m.removeEventListener("change", notify);
    },
    [query],
  );
  const snapshot = useCallback(() => window.matchMedia(query).matches, [query]);
  return useSyncExternalStore(subscribe, snapshot, () => false);
}
