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
  expect(savedCheckout(root, "")).toMatchObject(purchase);
  expect(savedCheckout(root, renewal.subToken)).toMatchObject(renewal);
  expect(savedCheckout(root, "", renewal.reference)).toMatchObject(renewal);
  expect(savedCheckout(root, "", "unknown")).toBeNull();

  remember(root, renewal, "clear");
  expect(savedCheckout(root, "")).toMatchObject(purchase);
  expect(savedCheckout(root, renewal.subToken)).toBeNull();
});

test("concurrent purchases keep separate keys and provider return references", () => {
  const second = { key: "e".repeat(64), subToken: "", reference: "f".repeat(64) };
  expect(remember(root, { ...purchase, reference: undefined })).toBe(true);
  expect(remember(root, { ...second, reference: undefined })).toBe(true);
  expect(remember(root, second, "update")).toBe(true);
  expect(remember(root, purchase, "update")).toBe(true);
  expect(savedCheckout(root, "", purchase.reference)).toMatchObject(purchase);
  expect(savedCheckout(root, "", second.reference)).toMatchObject(second);

  remember(root, purchase, "clear");
  expect(savedCheckout(root, "", purchase.reference)).toBeNull();
  expect(savedCheckout(root, "")).toMatchObject(second);
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
