/** Chance that is the same for the same seed: a site made twice of one document is one site. */
export class Rng {
  private state: number;

  constructor(seed: string) {
    let h = 1779033703 ^ seed.length;
    for (let i = 0; i < seed.length; i++) {
      h = Math.imul(h ^ seed.charCodeAt(i), 3432918353);
      h = (h << 13) | (h >>> 19);
    }
    this.state = h >>> 0;
  }

  /** A number from 0 up to, not including, 1. */
  next(): number {
    this.state = (this.state + 0x6d2b79f5) | 0;
    let t = Math.imul(this.state ^ (this.state >>> 15), 1 | this.state);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  }

  /** A whole number from lo to hi, both included. */
  int(lo: number, hi: number): number {
    return lo + Math.floor(this.next() * (hi - lo + 1));
  }

  chance(p: number): boolean {
    return this.next() < p;
  }

  pick<T>(list: readonly T[]): T {
    return list[Math.floor(this.next() * list.length)]!;
  }

  shuffled<T>(list: readonly T[]): T[] {
    const out = [...list];
    for (let i = out.length - 1; i > 0; i--) {
      const j = Math.floor(this.next() * (i + 1));
      [out[i], out[j]] = [out[j]!, out[i]!];
    }
    return out;
  }

  /** From lo to hi items of list, in an order of chance. */
  take<T>(list: readonly T[], [lo, hi]: [number, number]): T[] {
    return this.shuffled(list).slice(0, Math.min(list.length, this.int(lo, hi)));
  }
}

/** A seed nobody else has: the installation's own and what the browser draws at random. */
export function freshSeed(installation: string): string {
  const raw = new Uint32Array(4);
  crypto.getRandomValues(raw);
  return installation.slice(0, 12) + Array.from(raw, (n) => n.toString(36)).join("");
}
