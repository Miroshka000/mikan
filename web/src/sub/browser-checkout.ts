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
    const legacyKeys = [storageKey(root), storageKey(root) + ":" + checkout.subToken];
    const legacyKey = legacyKeys.find((k) => readCheckout(k)?.key === checkout.key);
    if (operation === "clear") {
      localStorage.removeItem(key);
    } else {
      const current = readCheckout(key);
      if (current && current.reference === checkout.reference) return true;
      const previous = current ?? (legacyKey && readCheckout(legacyKey));
      if (!previous) return false;
      localStorage.setItem(key, JSON.stringify({ ...previous, reference: checkout.reference ?? previous.reference }));
    }
    if (legacyKey) localStorage.removeItem(legacyKey);
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
      if (!key?.startsWith(storageKey(root) + ":")) continue;
      const checkout = readCheckout(key);
      if (!checkout) continue;
      // The provider return URL identifies the order, and only this browser has its key.
      if (reference && checkout.reference === reference) return checkout;
      if (!reference && checkout.subToken === subToken && (checkout.startedAt ?? 0) >= (latest?.startedAt ?? 0)) latest = checkout;
    }
    if (reference || latest) return latest;
    const legacy = readCheckout(storageKey(root));
    return legacy && (!subToken || legacy.subToken === subToken) ? legacy : null;
  } catch {
    return null;
  }
}
