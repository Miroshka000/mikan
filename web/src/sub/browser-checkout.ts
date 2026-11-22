import type { BrowserOrder } from "./shop";

export type Checkout = { key: string; subToken: string; order?: BrowserOrder; reference?: string };
type SavedCheckout = Checkout & { startedAt?: number };
const storageKey = (root: string) => "mikan-checkout:" + root;
const recordKey = (root: string, key: string) => storageKey(root) + ":order:" + key;

function readCheckout(key: string): SavedCheckout | null {
  const raw = localStorage.getItem(key);
  if (!raw) return null;
  try {
    const value: SavedCheckout | null = JSON.parse(raw);
    return value && /^[a-f0-9]{64}$/.test(value.key) && typeof value.subToken === "string" ? value : null;
  } catch {
    return null;
  }
}

export function remember(root: string, checkout: Checkout, operation: "start" | "update" | "clear" = "start"): boolean {
  try {
    const key = recordKey(root, checkout.key);
    if (operation === "start") {
      localStorage.setItem(key, JSON.stringify({ ...checkout, startedAt: Date.now() }));
      return true;
    }
    if (operation === "clear") {
      localStorage.removeItem(key);
      return true;
    }
    const current = readCheckout(key);
    if (!current) return false;
    if (current.reference === checkout.reference) return true;
    localStorage.setItem(key, JSON.stringify({ ...current, reference: checkout.reference ?? current.reference }));
    return true;
  } catch {
    return false;
  }
}

export function savedCheckout(root: string, subToken: string, reference?: string | null): Checkout | null {
  try {
    let latest: SavedCheckout | null = null;
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (!key?.startsWith(storageKey(root) + ":order:")) continue;
      const checkout = readCheckout(key);
      if (!checkout) continue;
      // The provider return URL identifies the order, and only this browser has its key.
      if (reference && checkout.reference === reference) return checkout;
      if (!reference && checkout.subToken === subToken && (checkout.startedAt ?? 0) >= (latest?.startedAt ?? 0)) latest = checkout;
    }
    return latest;
  } catch {
    return null;
  }
}
