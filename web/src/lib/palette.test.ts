import { describe, expect, it } from "vitest";
import { isPaletteKey, matches, step, userIdOf } from "./palette";

const key = (k: Partial<KeyboardEvent>) => ({ key: "k", code: "KeyK", ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...k });

describe("isPaletteKey", () => {
  it("is Ctrl+K, and ⌘K on a Mac", () => {
    expect(isPaletteKey(key({ ctrlKey: true }), false)).toBe(true);
    expect(isPaletteKey(key({ metaKey: true }), true)).toBe(true);
  });
  it("is the physical key in another layout", () => {
    expect(isPaletteKey(key({ key: "л", ctrlKey: true }), false)).toBe(true);
    expect(isPaletteKey(key({ key: "K", code: "", ctrlKey: true }), false)).toBe(true);
  });
  it("is not the other modifier, more of them, or another key", () => {
    expect(isPaletteKey(key({ metaKey: true }), false)).toBe(false);
    expect(isPaletteKey(key({ ctrlKey: true }), true)).toBe(false);
    expect(isPaletteKey(key({ ctrlKey: true, metaKey: true }), false)).toBe(false);
    expect(isPaletteKey(key({ ctrlKey: true, shiftKey: true }), false)).toBe(false);
    expect(isPaletteKey(key({ ctrlKey: true, altKey: true }), false)).toBe(false);
    expect(isPaletteKey(key({}), false)).toBe(false);
    expect(isPaletteKey(key({ key: "j", code: "KeyJ", ctrlKey: true }), false)).toBe(false);
  });
});

describe("matches", () => {
  it("finds every word in any order and case", () => {
    expect(matches("Nodes nodes", "NOD")).toBe(true);
    expect(matches("Настройки settings", "set")).toBe(true);
    expect(matches("Frankfurt de-1.example.com", "example fra")).toBe(true);
    expect(matches("Frankfurt", "fra berlin")).toBe(false);
  });
  it("takes ё for е", () => {
    expect(matches("Пётр", "петр")).toBe(true);
    expect(matches("Петр", "пётр")).toBe(true);
  });
  it("matches all with no words", () => {
    expect(matches("anything", "  ")).toBe(true);
  });
});

describe("userIdOf", () => {
  it("is the id typed as a number", () => {
    expect(userIdOf("12")).toBe(12);
    expect(userIdOf(" #7 ")).toBe(7);
  });
  it("is none for a name, zero or a number too long", () => {
    for (const q of ["", "anna", "12a", "#", "0", "-3", "1.5", "9999999999999999"]) {
      expect(userIdOf(q), q).toBeUndefined();
    }
  });
});

describe("step", () => {
  it("moves and wraps around", () => {
    expect(step(0, 3, 1)).toBe(1);
    expect(step(2, 3, 1)).toBe(0);
    expect(step(0, 3, -1)).toBe(2);
  });
  it("stays at 0 on an empty list", () => {
    expect(step(0, 0, 1)).toBe(0);
    expect(step(0, 0, -1)).toBe(0);
  });
});
