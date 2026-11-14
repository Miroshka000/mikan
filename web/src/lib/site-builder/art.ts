import { esc, f1 } from "./html";
import type { Rng } from "./rng";

export type Ink = { ink: string; accent: string; surface: string };

/**
 * A plotter drawing: lines made by one of a few rules, every number from the site's chance.
 * It stands where a gallery has a caption and no picture of the admin's own.
 */
export class PlotArt {
  constructor(
    private readonly rng: Rng,
    private readonly colors: Ink,
    private readonly w = 320,
    private readonly h = 240,
  ) {}

  svg(label: string): string {
    const rule = [this.drift, this.grid, this.rings, this.hatch][this.rng.int(0, 3)]!;
    return `<svg viewBox="0 0 ${this.w} ${this.h}" role="img" aria-label="${esc(label)}" xmlns="http://www.w3.org/2000/svg" fill="none" stroke-linecap="round" stroke-linejoin="round"><rect width="${this.w}" height="${this.h}" fill="${this.colors.surface}"/>${rule.call(this)}</svg>`;
  }

  /** Horizontal lines that drift like water. */
  private drift(): string {
    const { rng: r, w, h, colors: a } = this;
    const rows = r.int(14, 26);
    const amp = 4 + r.next() * 16;
    const freq = 0.01 + r.next() * 0.03;
    const phase = r.next() * 6;
    const every = r.int(5, 9);
    let out = "";
    for (let i = 1; i < rows; i++) {
      const y = (h / rows) * i;
      let d = "";
      for (let x = 24; x <= w - 24; x += 8) {
        const yy = y + Math.sin(x * freq + phase + i * 0.35) * amp * Math.sin((i / rows) * Math.PI);
        d += (d ? "L" : "M") + f1(x) + " " + f1(yy);
      }
      out += `<path d="${d}" stroke="${i % every === 0 ? a.accent : a.ink}" stroke-width="${r.chance(0.2) ? 1.6 : 0.9}"/>`;
    }
    return out;
  }

  /** A grid of squares with a fault in it. */
  private grid(): string {
    const { rng: r, w, h, colors: a } = this;
    const n = r.int(7, 12);
    const m = Math.max(3, Math.round(n * (h / w)));
    const cw = (w - 48) / n;
    const fx = r.int(1, n - 2);
    const fy = r.int(1, m - 2);
    let out = "";
    for (let i = 0; i < n; i++)
      for (let j = 0; j < m; j++) {
        const far = Math.hypot(i - fx, j - fy);
        const turn = far < 3 ? (3 - far) * (8 + r.next() * 14) : r.next() * 2;
        const s = cw * (0.55 + r.next() * 0.2);
        const cx = 24 + cw * (i + 0.5);
        const cy = 24 + cw * (j + 0.5);
        out += `<rect x="${f1(cx - s / 2)}" y="${f1(cy - s / 2)}" width="${f1(s)}" height="${f1(s)}" transform="rotate(${f1(turn)} ${f1(cx)} ${f1(cy)})" stroke="${far < 1 ? a.accent : a.ink}" stroke-width="1"/>`;
      }
    return out;
  }

  /** Rings around a point off the centre. */
  private rings(): string {
    const { rng: r, w, h, colors: a } = this;
    const cx = w * (0.3 + r.next() * 0.4);
    const cy = h * (0.3 + r.next() * 0.4);
    const n = r.int(9, 18);
    const marked = r.int(2, n);
    let out = "";
    for (let i = 1; i <= n; i++) {
      const rad = (Math.max(w, h) * 0.62 * i) / n;
      out += `<circle cx="${f1(cx + i * (r.next() - 0.5) * 3)}" cy="${f1(cy + i * (r.next() - 0.5) * 3)}" r="${f1(rad)}" stroke="${i === marked ? a.accent : a.ink}" stroke-width="0.9"${r.chance(0.3) ? ` stroke-dasharray="${r.int(2, 9)} ${r.int(3, 8)}"` : ""}/>`;
    }
    return out;
  }

  /** Bands of hatching, each leaning its own way. */
  private hatch(): string {
    const { rng: r, w, h, colors: a } = this;
    const bands = r.int(3, 6);
    const bh = (h - 48) / bands;
    const marked = r.int(0, bands - 1);
    let out = "";
    for (let b = 0; b < bands; b++) {
      const step = 4 + r.next() * 9;
      const lean = (r.next() - 0.5) * bh * 1.4;
      for (let x = 24; x < w - 24; x += step) {
        out += `<path d="M${f1(x)} ${f1(24 + bh * b + 4)}L${f1(Math.min(w - 24, Math.max(24, x + lean)))} ${f1(24 + bh * (b + 1) - 4)}" stroke="${b === marked ? a.accent : a.ink}" stroke-width="0.9"/>`;
      }
    }
    return out;
  }
}
