import { useMemo, useRef, useState } from "react";
import type { TrafficPoint } from "../api/client";
import { t } from "../i18n";
import { bytes, dateShort, time } from "../lib/format";

type Pt = { x: number; y: number };

// Monotone cubic interpolation (Fritsch–Carlson): smooth without overshooting below zero.
function monotone(p: Pt[]): string {
  const n = p.length;
  if (n === 0) return "";
  if (n === 1) return `M${p[0]!.x},${p[0]!.y}`;
  const dx: number[] = [];
  const m: number[] = [];
  const t: number[] = [];
  for (let i = 0; i < n - 1; i++) {
    dx[i] = p[i + 1]!.x - p[i]!.x;
    m[i] = (p[i + 1]!.y - p[i]!.y) / dx[i]!;
  }
  t[0] = m[0]!;
  t[n - 1] = m[n - 2]!;
  for (let i = 1; i < n - 1; i++) t[i] = m[i - 1]! * m[i]! <= 0 ? 0 : (m[i - 1]! + m[i]!) / 2;
  for (let i = 0; i < n - 1; i++) {
    if (m[i] === 0) {
      t[i] = 0;
      t[i + 1] = 0;
      continue;
    }
    const a = t[i]! / m[i]!;
    const b = t[i + 1]! / m[i]!;
    const s = a * a + b * b;
    if (s > 9) {
      const k = 3 / Math.sqrt(s);
      t[i] = k * a * m[i]!;
      t[i + 1] = k * b * m[i]!;
    }
  }
  let d = `M${p[0]!.x.toFixed(1)},${p[0]!.y.toFixed(1)}`;
  for (let i = 0; i < n - 1; i++) {
    const h = dx[i]!;
    d += ` C${(p[i]!.x + h / 3).toFixed(1)},${(p[i]!.y + (t[i]! * h) / 3).toFixed(1)} ${(p[i + 1]!.x - h / 3).toFixed(1)},${(p[i + 1]!.y - (t[i + 1]! * h) / 3).toFixed(1)} ${p[i + 1]!.x.toFixed(1)},${p[i + 1]!.y.toFixed(1)}`;
  }
  return d;
}

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const e = Math.pow(10, Math.floor(Math.log10(v)));
  const f = v / e;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10) * e;
}

export type Range = "24h" | "7d" | "30d";

/** Fills gaps with zero buckets so quiet hours are drawn as zero, not interpolated. */
export function buckets(points: TrafficPoint[], range: Range, now = Date.now()): TrafficPoint[] {
  const step = range === "30d" ? 86_400_000 : 3_600_000;
  const count = range === "24h" ? 24 : range === "7d" ? 168 : 30;
  const end = Math.floor(now / step) * step;
  const byT = new Map(points.map((p) => [Math.floor(new Date(p.t).getTime() / step) * step, p]));
  const out: TrafficPoint[] = [];
  for (let i = count - 1; i >= 0; i--) {
    const t = end - i * step;
    const p = byT.get(t);
    out.push({ t: new Date(t).toISOString(), up: p?.up ?? 0, down: p?.down ?? 0 });
  }
  if (range !== "7d") return out;
  // 168 hourly points are too dense to hover; merge into 2-hour buckets.
  const merged: TrafficPoint[] = [];
  for (let i = 0; i < out.length; i += 2) {
    const a = out[i]!;
    const b = out[i + 1];
    merged.push({ t: a.t, up: a.up + (b?.up ?? 0), down: a.down + (b?.down ?? 0) });
  }
  return merged;
}

const W = 720;
const H = 248;
const PAD = { l: 60, r: 8, t: 12, b: 28 };

export function TrafficChart({ points, range }: { points: TrafficPoint[]; range: Range }) {
  const [hover, setHover] = useState<number | null>(null);
  const ref = useRef<SVGSVGElement>(null);
  const g = useMemo(() => {
    const n = points.length;
    const max = niceMax(Math.max(1, ...points.map((p) => p.down), ...points.map((p) => p.up)) * 1.08);
    const x = (i: number) => PAD.l + ((W - PAD.l - PAD.r) * i) / Math.max(1, n - 1);
    const y = (v: number) => PAD.t + (H - PAD.t - PAD.b) * (1 - v / max);
    const down = monotone(points.map((p, i) => ({ x: x(i), y: y(p.down) })));
    const up = monotone(points.map((p, i) => ({ x: x(i), y: y(p.up) })));
    return { n, max, x, y, down, up, base: y(0) };
  }, [points]);

  const label = (iso: string) => (range === "30d" ? dateShort(iso) : range === "7d" ? `${dateShort(iso)}, ${time(iso)}` : time(iso));
  const tickEvery = range === "24h" ? 4 : range === "7d" ? 12 : 5;

  const onMove = (e: React.PointerEvent) => {
    const r = ref.current?.getBoundingClientRect();
    if (!r || g.n < 2) return;
    const px = ((e.clientX - r.left) * W) / r.width;
    const i = Math.round(((px - PAD.l) / (W - PAD.l - PAD.r)) * (g.n - 1));
    setHover(Math.max(0, Math.min(g.n - 1, i)));
  };

  const hp = hover !== null ? points[hover] : undefined;
  const scale = ref.current ? ref.current.getBoundingClientRect().width / W : 1;

  return (
    <div className="relative" style={{ margin: "0 -4px" }}>
      <svg ref={ref} viewBox={`0 0 ${W} ${H}`} className="block h-auto w-full overflow-visible" role="img" aria-label={t("chart.label")}>
        <defs>
          <linearGradient id="g-down" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0" style={{ stopColor: "var(--mikan-500)", stopOpacity: 0.24 }} />
            <stop offset="1" style={{ stopColor: "var(--mikan-500)", stopOpacity: 0 }} />
          </linearGradient>
          <linearGradient id="g-up" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0" style={{ stopColor: "var(--lagoon-500)", stopOpacity: 0.2 }} />
            <stop offset="1" style={{ stopColor: "var(--lagoon-500)", stopOpacity: 0 }} />
          </linearGradient>
        </defs>
        {[0, 1, 2, 3, 4].map((k) => {
          const v = (g.max * k) / 4;
          return (
            <g key={k}>
              <line x1={PAD.l} x2={W - PAD.r} y1={g.y(v)} y2={g.y(v)} stroke="var(--hairline)" />
              <text x={PAD.l - 10} y={g.y(v) + 4} textAnchor="end" className="fill-[var(--ink-400)] text-[11px]">
                {k === 0 ? "0" : bytes(v)}
              </text>
            </g>
          );
        })}
        {points.map((p, i) =>
          i % tickEvery === 0 || i === g.n - 1 ? (
            <text key={p.t} x={g.x(i)} y={H - 6} textAnchor={i === 0 ? "start" : i === g.n - 1 ? "end" : "middle"} className="fill-[var(--ink-400)] text-[11px]">
              {label(p.t)}
            </text>
          ) : null,
        )}
        {g.n > 1 ? (
          <>
            <path d={`${g.down} L${g.x(g.n - 1)},${g.base} L${g.x(0)},${g.base} Z`} fill="url(#g-down)" />
            <path d={`${g.up} L${g.x(g.n - 1)},${g.base} L${g.x(0)},${g.base} Z`} fill="url(#g-up)" />
            <path d={g.down} fill="none" stroke="var(--mikan-500)" strokeWidth={2.25} strokeLinecap="round" />
            <path d={g.up} fill="none" stroke="var(--lagoon-500)" strokeWidth={2} strokeLinecap="round" />
          </>
        ) : null}
        {hp && hover !== null ? (
          <g>
            <line x1={g.x(hover)} x2={g.x(hover)} y1={PAD.t} y2={g.base} stroke="var(--ink-300)" strokeDasharray="3 3" />
            <circle cx={g.x(hover)} cy={g.y(hp.down)} r={4.5} fill="var(--surface-solid)" stroke="var(--mikan-500)" strokeWidth={2} />
            <circle cx={g.x(hover)} cy={g.y(hp.up)} r={4} fill="var(--surface-solid)" stroke="var(--lagoon-500)" strokeWidth={2} />
          </g>
        ) : null}
        <rect x={PAD.l} y={0} width={W - PAD.l - PAD.r} height={H} fill="transparent" onPointerMove={onMove} onPointerLeave={() => setHover(null)} />
      </svg>
      {hp && hover !== null ? (
        <div
          className="glass-strong pointer-events-none absolute rounded-xl px-3 py-2 text-xs leading-5"
          style={{ left: Math.min(Math.max(g.x(hover) * scale - 80, 0), (ref.current?.getBoundingClientRect().width ?? 0) - 170), top: Math.max(0, g.y(hp.down) * scale - 80), minWidth: 160 }}
        >
          <b className="font-semibold">{label(hp.t)}</b>
          <div className="flex justify-between gap-3">
            <span>{t("chart.down")}</span>
            <span className="num">{bytes(hp.down)}</span>
          </div>
          <div className="flex justify-between gap-3">
            <span>{t("chart.up")}</span>
            <span className="num">{bytes(hp.up)}</span>
          </div>
        </div>
      ) : null}
    </div>
  );
}
