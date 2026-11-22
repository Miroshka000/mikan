import type { BrowserOrder } from "./shop";

export type Checkout = { key: string; subToken: string; order?: BrowserOrder; reference?: string };
const storageKey = (root: string) => "mikan-checkout:" + root;
const scopedKey = (root: string, subToken: string) => storageKey(root) + ":" + subToken;

function readCheckout(key: string): Checkout | null {
  const raw = localStorage.getItem(key);
  if (!raw) return null;
  try {
    const value: Checkout | null = JSON.parse(raw);
    return value && /^[a-f0-9]{64}$/.test(value.key) && typeof value.subToken === "string" ? value : null;
  } catch {
    return null;
  }
}

export function remember(root: string, checkout: Checkout, operation: "start" | "update" | "clear" = "start"): boolean {
  try {
    const key = scopedKey(root, checkout.subToken);
    if (operation === "start") {
      localStorage.setItem(key, JSON.stringify(checkout));
      return true;
    }
    const current = readCheckout(key);
    const legacyKey = storageKey(root);
    const legacyMatches = readCheckout(legacyKey)?.key === checkout.key;
    if (operation === "clear") {
      if (current?.key === checkout.key) localStorage.removeItem(key);
    } else if (current?.key === checkout.key || (!current && legacyMatches)) {
      localStorage.setItem(key, JSON.stringify(checkout));
    } else {
      return false;
    }
    if (legacyMatches) localStorage.removeItem(legacyKey);
    return true;
  } catch {
    return false;
  }
}

export function savedCheckout(root: string, subToken: string, reference?: string | null): Checkout | null {
  try {
    if (reference) {
      // The provider return URL identifies the order, but only this browser has its key.
      for (let i = 0; i < localStorage.length; i++) {
        const key = localStorage.key(i);
        if (!key?.startsWith(storageKey(root) + ":")) continue;
        const checkout = readCheckout(key);
        if (checkout?.reference === reference) return checkout;
      }
      return null;
    }
    const scoped = readCheckout(scopedKey(root, subToken));
    if (scoped) return scoped;
    const legacy = readCheckout(storageKey(root));
    return legacy && (!subToken || legacy.subToken === subToken) ? legacy : null;
  } catch {
    return null;
  }
}
