import { describe, expect, it } from "vitest";
import { hopMemoryMB, hopPorts, MAX_HOP_PORTS } from "./hop";

describe("hopPorts", () => {
  it("counts a port and every port of a range", () => {
    expect(hopPorts("443")).toBe(1);
    expect(hopPorts("20000-20099")).toBe(100);
    expect(hopPorts(" 20000-20999 ")).toBe(MAX_HOP_PORTS);
    expect(hopPorts("20000-30000")).toBe(10001);
  });

  it("takes nothing the panel refuses", () => {
    for (const spec of ["", "0", "70000", "2083-2000", "2000-2000", "20000-70000", "a-b", "443,444"]) {
      expect(hopPorts(spec)).toBe(0);
    }
  });
});

describe("hopMemoryMB", () => {
  it("is about 0.12 MB a port", () => {
    expect(hopMemoryMB(10001)).toBe(1200);
    expect(hopMemoryMB(100)).toBe(12);
  });
});
