import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { remember, savedCheckout } from "./browser-checkout";

const root = "https://store.example/sub/shop";
const purchase = { key: "a".repeat(64), subToken: "", reference: "c".repeat(64) };
const renewal = { key: "b".repeat(64), subToken: "subscription-token", reference: "d".repeat(64) };

beforeEach(() => {
  const entries = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    get length() { return entries.size; },
    key: (index: number) => [...entries.keys()][index] ?? null,
    getItem: (key: string) => entries.get(key) ?? null,
    setItem: (key: string, value: string) => { entries.set(key, value); },
    removeItem: (key: string) => { entries.delete(key); },
  });
});

afterEach(() => vi.unstubAllGlobals());

test("a purchase and a renewal can both be recovered, including from a provider return", () => {
  expect(remember(root, purchase)).toBe(true);
  expect(remember(root, renewal)).toBe(true);
  expect(savedCheckout(root, "")).toEqual(purchase);
  expect(savedCheckout(root, renewal.subToken)).toEqual(renewal);
  expect(savedCheckout(root, "", renewal.reference)).toEqual(renewal);
  expect(savedCheckout(root, "", "unknown")).toBeNull();

  remember(root, renewal, "clear");
  expect(savedCheckout(root, "")).toEqual(purchase);
  expect(savedCheckout(root, renewal.subToken)).toBeNull();
});

test("resuming an older saved order adds its provider return reference", () => {
  localStorage.setItem(`mikan-checkout:${root}`, JSON.stringify({ key: renewal.key, subToken: renewal.subToken }));
  expect(savedCheckout(root, renewal.subToken)?.key).toBe(renewal.key);
  expect(remember(root, renewal, "update")).toBe(true);
  expect(savedCheckout(root, "", renewal.reference)).toEqual(renewal);
});

test("a blocked storage write is reported to the caller without throwing", () => {
  vi.spyOn(localStorage, "setItem").mockImplementation(() => { throw new Error("storage denied"); });
  expect(remember(root, purchase)).toBe(false);
});
