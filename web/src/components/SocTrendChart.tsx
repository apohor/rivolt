import { useEffect, useMemo, useRef, useState } from "react";

// Battery (SoC) trend for the ui_theme_next layer: a smooth line with a fading
// fill over coloured bands for charging (emerald) and driving (sky), the charge
// limit labelled at the right edge, min/max markers and a crosshair tooltip.
type Pt = { x: number; y: number };
type Span = { x0: number; x1: number };

const H = 190;
const PAD = { l: 34, r: 54, t: 12, b: 22 };

function monotone(p: [number, number][]): string {
  const n = p.length;
  if (n === 0) return "";
  if (n === 1) return `M ${p[0][0]} ${p[0][1]}`;
  const dx: number[] = [];
  const m: number[] = [];
  for (let i = 0; i < n - 1; i++) {
    dx.push(p[i + 1][0] - p[i][0] || 1e-6);
    m.push((p[i + 1][1] - p[i][1]) / dx[i]);
  }
  const t: number[] = [m[0]];
  for (let i = 1; i < n - 1; i++) t.push(m[i - 1] * m[i] <= 0 ? 0 : (m[i - 1] + m[i]) / 2);
  t.push(m[n - 2]);
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
  let d = `M ${p[0][0].toFixed(1)} ${p[0][1].toFixed(1)}`;
  for (let i = 0; i < n - 1; i++) {
    const h = dx[i] / 3;
    d += ` C ${(p[i][0] + h).toFixed(1)} ${(p[i][1] + t[i] * h).toFixed(1)} ${(p[i + 1][0] - h).toFixed(1)} ${(p[i + 1][1] - t[i + 1] * h).toFixed(1)} ${p[i + 1][0].toFixed(1)} ${p[i + 1][1].toFixed(1)}`;
  }
  return d;
}

export function SocTrendChart({
  trend,
  charges,
  drives,
  domain,
  limit,
}: {
  trend: Pt[];
  charges: Span[];
  drives: Span[];
  domain: [number, number] | null;
  limit: number | null;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(600);
  const [hover, setHover] = useState<number | null>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const set = () => setW(Math.max(260, Math.round(el.clientWidth)));
    set();
    const ro = new ResizeObserver(set);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const [x0, x1] = useMemo<[number, number]>(() => {
    if (domain && domain[1] > domain[0]) return domain;
    if (trend.length > 1) return [trend[0].x, trend[trend.length - 1].x];
    const now = Date.now();
    return [now - 86_400_000, now];
  }, [domain, trend]);
  const pts = useMemo(() => trend.filter((p) => p.x >= x0 && p.x <= x1), [trend, x0, x1]);
  const iw = w - PAD.l - PAD.r;
  const ih = H - PAD.t - PAD.b;
  const X = (x: number) => PAD.l + ((x - x0) / (x1 - x0)) * iw;
  const Y = (y: number) => PAD.t + (1 - y / 100) * ih;
  const xy = pts.map((p) => [X(p.x), Y(p.y)] as [number, number]);
  const line = monotone(xy);
  const area = xy.length > 1 ? `${line} L ${xy[xy.length - 1][0].toFixed(1)} ${Y(0)} L ${xy[0][0].toFixed(1)} ${Y(0)} Z` : "";
  const clip = (s: Span) => ({ a: Math.max(s.x0, x0), b: Math.min(s.x1, x1) });
  // A band is a faint wash over the plot plus a solid strip on the bottom axis, with a
  // minimum width so a short session is still a visible mark at month and year scale.
  const STRIP = 6;
  const band = (s: Span, i: number, color: string, kind: string) => {
    const { a, b } = clip(s);
    if (b <= a) return null;
    const wd = Math.max(3, X(b) - X(a));
    return (
      <g key={`${kind}${i}`} data-band={kind} data-draw={kind}>
        <rect x={X(a)} y={PAD.t} width={wd} height={ih} fill={color} opacity={0.1} />
        <rect x={X(a)} y={PAD.t + ih - STRIP} width={wd} height={STRIP} rx={1} fill={color} />
      </g>
    );
  };
  const drawnCharge = charges.some((s) => clip(s).b > clip(s).a);
  const drawnDrive = drives.some((s) => clip(s).b > clip(s).a);
  const lo = pts.reduce<Pt | null>((m, p) => (m == null || p.y < m.y ? p : m), null);
  const hi = pts.reduce<Pt | null>((m, p) => (m == null || p.y > m.y ? p : m), null);
  const day = (ms: number) => new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  const nt = Math.max(2, Math.min(5, Math.floor(iw / 110)));
  const ticks = Array.from({ length: nt }, (_, i) => x0 + ((x1 - x0) * i) / (nt - 1));

  const at = (x: number) => {
    // No reading outside the recorded span (the crosshair would otherwise float over empty space).
    if (pts.length === 0 || x < pts[0].x || x > pts[pts.length - 1].x) return null;
    let i = pts.findIndex((p) => p.x >= x);
    if (i <= 0) i = i === 0 ? 0 : pts.length - 1;
    const a = pts[Math.max(0, i - 1)];
    const b = pts[i];
    const y = b.x === a.x ? b.y : a.y + ((b.y - a.y) * (x - a.x)) / (b.x - a.x);
    const inC = charges.some((s) => x >= s.x0 && x <= s.x1);
    const inD = drives.some((s) => x >= s.x0 && x <= s.x1);
    return { y, state: inC ? "Charging" : inD ? "Driving" : "Parked" };
  };
  const hv = hover != null ? at(hover) : null;
  const move = (e: React.PointerEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const px = ((e.clientX - r.left) / r.width) * w;
    setHover(Math.min(x1, Math.max(x0, x0 + ((px - PAD.l) / iw) * (x1 - x0))));
  };

  return (
    <div ref={ref} className="relative" data-soc-trend>
      <svg
        viewBox={`0 0 ${w} ${H}`}
        width={w}
        height={H}
        role="img"
        aria-label="Battery state of charge over time, with charging and driving shaded"
        onPointerMove={move}
        onPointerLeave={() => setHover(null)}
        className="touch-pan-y"
      >
        <defs>
          <linearGradient id="soc-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="currentColor" stopOpacity="0.2" />
            <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
          </linearGradient>
        </defs>
        {[0, 50, 100].map((v) => (
          <g key={v}>
            <line x1={PAD.l} x2={PAD.l + iw} y1={Y(v)} y2={Y(v)} className="text-neutral-800" stroke="currentColor" strokeOpacity={0.5} />
            <text x={PAD.l - 6} y={Y(v) + 3.5} textAnchor="end" className="fill-neutral-500 text-[10px]">{v}%</text>
          </g>
        ))}
        {charges.map((s, i) => band(s, i, "#10b981", "charge"))}
        {drives.map((s, i) => band(s, i, "#38bdf8", "drive"))}
        {limit != null && limit > 0 && limit < 100 && (
          <g data-testid="soc-limit" data-draw="limit">
            <line x1={PAD.l} x2={PAD.l + iw} y1={Y(limit)} y2={Y(limit)} stroke="#f59e0b" strokeDasharray="4 3" strokeWidth={1} />
            <text x={PAD.l + iw + 4} y={Y(limit) + 3.5} className="fill-amber-400 text-[10px]">Limit {Math.round(limit)}%</text>
          </g>
        )}
        {area && <path d={area} fill="url(#soc-fill)" className="text-neutral-100" />}
        {line && <path d={line} data-draw="line" fill="none" stroke="currentColor" className="text-neutral-100" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />}
        {hi && lo && hi.x !== lo.x && (
          <g data-draw="extreme">
            <circle cx={X(hi.x)} cy={Y(hi.y)} r={3.5} fill="currentColor" stroke="currentColor" className="text-neutral-100" />
            <text x={X(hi.x)} y={Y(hi.y) - 7} textAnchor="middle" className="fill-neutral-300 text-[10px]" data-testid="soc-max">{Math.round(hi.y)}%</text>
            <circle cx={X(lo.x)} cy={Y(lo.y)} r={3.5} fill="currentColor" stroke="currentColor" className="text-neutral-100" />
            <text x={X(lo.x)} y={Y(lo.y) + 15} textAnchor="middle" className="fill-neutral-300 text-[10px]" data-testid="soc-min">{Math.round(lo.y)}%</text>
          </g>
        )}
        {ticks.map((t, i) => (
          <text key={i} x={X(t)} y={H - 6} textAnchor={i === 0 ? "start" : i === ticks.length - 1 ? "end" : "middle"} className="fill-neutral-500 text-[10px]">{day(t)}</text>
        ))}
        {hover != null && hv && (
          <g pointerEvents="none">
            <line x1={X(hover)} x2={X(hover)} y1={PAD.t} y2={PAD.t + ih} stroke="currentColor" className="text-neutral-400" strokeOpacity={0.6} />
            <circle cx={X(hover)} cy={Y(hv.y)} r={4} fill="currentColor" stroke="currentColor" className="text-neutral-100" />
          </g>
        )}
      </svg>
      {hover != null && hv && (
        <div
          role="status"
          data-testid="soc-tip"
          className="pointer-events-none absolute top-0 z-10 whitespace-nowrap rounded-md border border-neutral-700 bg-neutral-900/95 px-2 py-1 text-xs text-neutral-200 shadow-lg"
          style={{ left: Math.min(Math.max(X(hover) - 50, 0), w - 120) }}
        >
          <span>{new Date(hover).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}</span>
          {" · "}
          <span className="font-medium">{Math.round(hv.y)}%</span>
          {" · "}
          {hv.state}
        </div>
      )}
      <p data-soc-legend className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-neutral-500">
        {line && (
          <span data-legend="line">
            <span aria-hidden className="mr-1 inline-block h-0 w-4 border-t-2 align-middle border-neutral-100" />
            Battery %
          </span>
        )}
        {drawnCharge && (
          <span data-legend="charge">
            <span aria-hidden className="mr-1 inline-block h-1.5 w-4 rounded-[1px] align-middle" style={{ background: "#10b981" }} />
            Charging
          </span>
        )}
        {drawnDrive && (
          <span data-legend="drive">
            <span aria-hidden className="mr-1 inline-block h-1.5 w-4 rounded-[1px] align-middle" style={{ background: "#38bdf8" }} />
            Driving
          </span>
        )}
        {limit != null && limit > 0 && limit < 100 && (
          <span data-legend="limit">
            <span aria-hidden className="mr-1 inline-block h-0 w-4 border-t border-dashed align-middle" style={{ borderColor: "#f59e0b" }} />
            Charge limit
          </span>
        )}
        {hi && lo && hi.x !== lo.x && (
          <span data-legend="extreme">
            <span aria-hidden className="mr-1 inline-block h-2 w-2 rounded-full bg-neutral-100 align-middle" />
            High and low
          </span>
        )}
      </p>
    </div>
  );
}
