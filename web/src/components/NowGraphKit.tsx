import { useCallback, useId, useRef, useState } from "react";
import { useIsLight } from "../lib/vehicleImage";

// The Now card's soft graph look (the owner-approved parked graph): one soft line with round caps and a light glow over a faint fill, labelled
// start and current values instead of an axis, small haloed dots for events, and soft low-contrast bands. The charging and driving graphs draw
// with these pieces so the Now card's graphs match; text uses the app's own sizes (text-xs, text-sm) and its neutral ink.

// hold: the line leaves and arrives level at this point (zero slope), which is how a step is eased instead of drawn as a corner.
export type Pt = { x: number; y: number; hold?: boolean };

export type SoftInk = { line: string; quiet: string; warm: string; band: string };

// Dark: the mock's mint line on a near-black card. Light: the same hues one step deeper, so the line holds on a white card.
export function useSoftInk(): SoftInk {
  const light = useIsLight();
  return light
    ? { line: "#059669", quiet: "#d97706", warm: "#b45309", band: "#6366f1" }
    : { line: "#6ee7b7", quiet: "#fbbf24", warm: "#fcd34d", band: "#818cf8" };
}

// The width and height of an element, as it is laid out: the chart draws in real pixels, so its text is the app's text size at any width.
export function useBox(): [(el: HTMLDivElement | null) => void, { w: number; h: number }] {
  const [box, setBox] = useState({ w: 0, h: 0 });
  const roRef = useRef<ResizeObserver | null>(null);
  const ref = useCallback((el: HTMLDivElement | null) => {
    roRef.current?.disconnect();
    roRef.current = null;
    if (!el || typeof ResizeObserver === "undefined") return;
    roRef.current = new ResizeObserver(([e]) => {
      const w = Math.round(e.contentRect.width);
      const h = Math.round(e.contentRect.height);
      setBox((b) => (b.w === w && b.h === h ? b : { w, h }));
    });
    roRef.current.observe(el);
  }, []);
  return [ref, box];
}

// Readings that jump (a drive or a charge between two readings, a wake-up) arrive a few pixels apart, which a curve can only draw as a corner.
// This spreads each such jump into a short eased S that ends on the second reading (so a dot there stays on the line) and starts level at the
// first one's height; softPath then draws it with no slope at both ends. A pair that is already gentle is left alone.
export function easeSteps(pts: Pt[], minDrop = 3, slope = 1.2): Pt[] {
  if (pts.length === 0) return [];
  const out: Pt[] = [pts[0]];
  for (let i = 1; i < pts.length; i++) {
    const a = pts[i - 1];
    const b = pts[i];
    const dy = Math.abs(b.y - a.y);
    if (dy < minDrop || dy / Math.max(b.x - a.x, 0.5) <= slope) {
      out.push(b);
      continue;
    }
    const run = Math.min(Math.max(dy * 2, 12), 28);
    const last = out[out.length - 1];
    if (last.hold) {
      // The first reading ends the jump before this one: it stays, and the next ramp starts level after it if there is room.
      const x = Math.max(b.x - run, last.x);
      if (x > last.x + 0.5) out.push({ x, y: a.y, hold: true });
    } else {
      const from = out.length > 1 ? out[out.length - 2].x : a.x;
      out[out.length - 1] = { x: Math.max(b.x - run, from + 0.4 * (a.x - from)), y: a.y, hold: true };
    }
    out.push({ x: b.x, y: b.y, hold: true });
  }
  return out;
}

// A smooth path through the points that never overshoots them (monotone cubic), so a flat stretch stays flat and a turn stays a turn.
export function softPath(pts: Pt[]): string {
  const n = pts.length;
  if (n === 0) return "";
  const f = (v: number) => v.toFixed(1);
  if (n === 1) return `M${f(pts[0].x)} ${f(pts[0].y)}`;
  const dx: number[] = [];
  const m: number[] = [];
  for (let i = 0; i < n - 1; i++) {
    dx.push(pts[i + 1].x - pts[i].x);
    m.push(dx[i] === 0 ? 0 : (pts[i + 1].y - pts[i].y) / dx[i]);
  }
  const t: number[] = [m[0]];
  for (let i = 1; i < n - 1; i++) t.push(m[i - 1] * m[i] <= 0 ? 0 : (m[i - 1] + m[i]) / 2);
  t.push(m[n - 2]);
  for (let i = 0; i < n; i++) if (pts[i].hold) t[i] = 0;
  for (let i = 0; i < n - 1; i++) {
    if (m[i] === 0) {
      t[i] = 0;
      t[i + 1] = 0;
      continue;
    }
    const a = t[i] / m[i];
    const b = t[i + 1] / m[i];
    const s = a * a + b * b;
    if (s > 9) {
      const k = 3 / Math.sqrt(s);
      t[i] = k * a * m[i];
      t[i + 1] = k * b * m[i];
    }
  }
  let d = `M${f(pts[0].x)} ${f(pts[0].y)}`;
  for (let i = 0; i < n - 1; i++) {
    const h = dx[i] / 3;
    d += ` C${f(pts[i].x + h)} ${f(pts[i].y + t[i] * h)} ${f(pts[i + 1].x - h)} ${f(pts[i + 1].y - t[i + 1] * h)} ${f(pts[i + 1].x)} ${f(pts[i + 1].y)}`;
  }
  return d;
}

// At most `max` points, evenly picked and always keeping the last: a path of a few dozen curves is as smooth as one of two thousand.
export function thin<T>(pts: T[], max = 48): T[] {
  if (pts.length <= max) return pts;
  const out: T[] = [];
  for (let i = 0; i < max - 1; i++) out.push(pts[Math.round((i * (pts.length - 1)) / (max - 1))]);
  out.push(pts[pts.length - 1]);
  return out;
}

// The shared gradients and blur filters, under ids unique to the chart that draws them.
export function SoftDefs({ id, ink }: { id: string; ink: SoftInk }) {
  return (
    <defs>
      <linearGradient id={`${id}-fill`} x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stopColor={ink.line} stopOpacity=".18" />
        <stop offset="1" stopColor={ink.line} stopOpacity="0" />
      </linearGradient>
      <linearGradient id={`${id}-band`} x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" stopColor={ink.band} stopOpacity=".08" />
        <stop offset="1" stopColor={ink.band} stopOpacity="0" />
      </linearGradient>
      <filter id={`${id}-glow`} x="-20%" y="-80%" width="140%" height="260%">
        <feGaussianBlur stdDeviation="4" result="b" />
        <feMerge>
          <feMergeNode in="b" />
          <feMergeNode in="SourceGraphic" />
        </feMerge>
      </filter>
      <filter id={`${id}-soften`} x="-30%" y="-30%" width="160%" height="160%">
        <feGaussianBlur stdDeviation="9" />
      </filter>
      <filter id={`${id}-halo`}>
        <feGaussianBlur stdDeviation="3" />
      </filter>
    </defs>
  );
}

export const useSoftId = () => useId().replace(/[^a-zA-Z0-9]/g, "");

// The main line: round caps, a light glow.
export function SoftLine({ id, d, color, width = 2.6 }: { id: string; d: string; color: string; width?: number }) {
  return <path d={d} fill="none" stroke={color} strokeWidth={width} strokeLinecap="round" strokeLinejoin="round" strokeOpacity=".95" filter={`url(#${id}-glow)`} />;
}

// A quieter second line: thinner, no glow.
export function QuietLine({ d, color }: { d: string; color: string }) {
  return <path d={d} fill="none" stroke={color} strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round" strokeOpacity=".6" />;
}

export function SoftFill({ id, d, baseY, x0, x1 }: { id: string; d: string; baseY: number; x0: number; x1: number }) {
  return <path d={`${d} L${x1.toFixed(1)} ${baseY} L${x0.toFixed(1)} ${baseY} Z`} fill={`url(#${id}-fill)`} />;
}

// A small dot with a soft halo; `big` is the live "now" dot.
export function HaloDot({ id, x, y, color, big = false }: { id: string; x: number; y: number; color: string; big?: boolean }) {
  return (
    <g>
      <circle cx={x} cy={y} r={big ? 10 : 7} fill={color} fillOpacity={big ? 0.35 : 0.3} filter={`url(#${id}-halo)`} />
      <circle cx={x} cy={y} r={big ? 4.5 : 3} fill={color} />
    </g>
  );
}

// A small rounded label on a faint plate, centred (or ended) at x.
export function Pill({ x, y, text, w, anchor = "middle" }: { x: number; y: number; text: string; w: number; anchor?: "middle" | "end" }) {
  const x0 = anchor === "end" ? x - w : x - w / 2;
  return (
    <g>
      <rect x={x0} y={y - 10} width={w} height={20} rx={10} className="fill-neutral-500/15" />
      <text x={x0 + w / 2} y={y + 4} textAnchor="middle" className="fill-neutral-300 text-xs">
        {text}
      </text>
    </g>
  );
}

// The width a short label needs at the app's small text, so a plate can wrap it.
export const labelWidth = (text: string) => Math.ceil(text.length * 6.6 + 20);

// "-2.4%" style header of a graph: a small caps title left, the change right.
export function GraphHead({ title, change, since, testid }: { title: string; change: string | null; since?: string; testid?: string }) {
  return (
    <div className="mb-1 flex flex-wrap items-baseline justify-between gap-x-3">
      <span className="ui-label">{title}</span>
      {change && (
        <span data-testid={testid} className="ml-auto text-sm font-semibold tabular-nums text-neutral-100">
          {change}
          {since && <span className="ml-1 text-xs font-normal text-neutral-400">{since}</span>}
        </span>
      )}
    </div>
  );
}

// The legend row under a graph: a soft swatch and a word each.
export function Legend({ items }: { items: { swatch: "line" | "dash" | "dot"; color: string; label: string; testid?: string }[] }) {
  return (
    <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-neutral-400">
      {items.map((it) => (
        <span key={it.label} data-testid={it.testid} className="inline-flex items-center gap-1.5">
          {it.swatch === "dot" ? (
            <i className="inline-block h-2 w-2 rounded-full" style={{ background: it.color }} />
          ) : (
            <i className={`inline-block w-3.5 ${it.swatch === "dash" ? "border-t-2 border-dashed" : "h-0.5 rounded"}`} style={it.swatch === "dash" ? { borderColor: it.color } : { background: it.color }} />
          )}
          {it.label}
        </span>
      ))}
    </div>
  );
}
