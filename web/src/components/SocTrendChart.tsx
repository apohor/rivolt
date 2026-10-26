import { useEffect, useMemo, useRef, useState } from "react";
import { axisDayIndices } from "../lib/period";
import { CHART_AXIS_PX } from "../lib/chartAxis";

// Battery (SoC) trend for the app, calm, the same chart as the iPhone's: a soft band for each day from
// its lowest to highest level (smoothed between days), one smooth line over it for where the day ended, only the
// period's highest and lowest points named inside the plot, one faint line for the charge limit named once, a tiny
// tick on the axis for each day it charged and a barely visible tint under 20%. A road trip reads as a deep, wide dip
// in the band. No bars, strip or legend: one caption says what the marks are.
type Pt = { x: number; y: number };
type Span = { x0: number; x1: number };
// A charge: its span and the limit it was set to (1–100; null or 0 unknown).
type ChargeSpan = Span & { limit?: number | null };
export type LimitStep = { x0: number; x1: number; pct: number };

// The chart's two colours (the design tokens read them here).
const SOC_GREEN = "#10b981";
const SOC_LIMIT = "#f59e0b";
const LOW_ZONE = 20;

const validLimit = (p: number | null | undefined): p is number => p != null && Number.isFinite(p) && p > 0 && p <= 100;

/** The charge limit as steps over [from, to), the same rule as the iPhone: each charge with a limit starts a step
 *  at its start, held to the next one with a limit (a charge without one carries the step on); nothing before the
 *  period's first such charge. After the last charge's end, up to now, the live target when known (the current
 *  period only), else that charge's limit. Steps end at now: days to come stay empty. Neighbouring steps at the
 *  same value are joined. */
export function limitSteps(charges: ChargeSpan[], from: number, to: number, now: number, live: number | null): LimitStep[] {
  const end = Math.min(to, now);
  const set = charges
    .filter((c) => validLimit(c.limit) && c.x0 >= from && c.x0 < end)
    .sort((a, b) => a.x0 - b.x0);
  const out: LimitStep[] = [];
  const push = (x0: number, x1: number, pct: number) => {
    if (x1 <= x0) return;
    const last = out[out.length - 1];
    if (last && last.pct === pct && last.x1 >= x0) last.x1 = Math.max(last.x1, x1);
    else out.push({ x0, x1, pct });
  };
  set.forEach((c, i) => {
    const pct = c.limit as number;
    const next = set[i + 1];
    if (next) {
      push(c.x0, next.x0, pct);
      return;
    }
    const held = Math.min(Math.max(c.x1, c.x0), end);
    push(c.x0, held, pct);
    const current = now >= from && now <= to;
    push(held, end, current && validLimit(live) ? live : pct);
  });
  return out;
}

const H = 200;
const PAD = { l: 34, r: 20, t: 10, b: 22 };
const Y_MAX = 104;
const DAY = 86_400_000;

/** Integer percent, half away from zero (2.5 → 3). */
export function pctLabel(v: number): string {
  return `${Math.sign(v) * Math.round(Math.abs(v))}%`;
}

/** The x-axis days, labelled as Miles per day (axisDayIndices: a month on the 1st, 11th, 21st and its last day,
 *  named "Oct 1"); longer spans, up to `n` evenly spaced dates. */
export function dayTicks(x0: number, x1: number, n: number): { x: number; label: string }[] {
  if (x1 - x0 <= 32 * DAY) {
    const days: Date[] = [];
    const d = new Date(x0);
    d.setHours(0, 0, 0, 0);
    if (d.getTime() < x0) d.setDate(d.getDate() + 1);
    for (; d.getTime() < x1; d.setDate(d.getDate() + 1)) days.push(new Date(d));
    const picked = axisDayIndices(days);
    if (picked) return picked.map((i) => ({ x: days[i].getTime(), label: dayName(days[i].getTime()) }));
  }
  const fmt = (ms: number) => new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return Array.from({ length: n }, (_, i) => x0 + ((x1 - x0) * i) / (n - 1)).map((x) => ({ x, label: fmt(x) }));
}

/** One local day with readings: how low and how high the level went, where it ended, and whether a charge started or
 *  ended on it. */
export type DayRange = { day: number; low: number; high: number; end: number; charged: boolean };

const startOfDay = (ms: number) => {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
};

/** The readings as one entry per local day that has any, oldest first. A reading of 0 or less is "unknown". */
export function dailyRanges(trend: Pt[], charges: Span[]): DayRange[] {
  const byDay = new Map<number, Pt[]>();
  for (const p of trend) {
    if (!Number.isFinite(p.y) || p.y <= 0) continue;
    const k = startOfDay(p.x);
    const list = byDay.get(k);
    if (list) list.push(p);
    else byDay.set(k, [p]);
  }
  const charged = new Set<number>();
  for (const c of charges) {
    charged.add(startOfDay(c.x0));
    charged.add(startOfDay(c.x1));
  }
  return [...byDay.keys()]
    .sort((a, b) => a - b)
    .map((day) => {
      const ps = (byDay.get(day) as Pt[]).slice().sort((a, b) => a.x - b.x);
      return { day, low: Math.min(...ps.map((p) => p.y)), high: Math.max(...ps.map((p) => p.y)), end: ps[ps.length - 1].y, charged: charged.has(day) };
    });
}

/** The day with the highest reading and the one with the lowest; null with fewer than two different levels. */
export function extremesOf(rows: DayRange[]): { hi: DayRange; lo: DayRange } | null {
  if (rows.length === 0) return null;
  const hi = rows.reduce((m, r) => (r.high > m.high ? r : m));
  const lo = rows.reduce((m, r) => (r.low < m.low ? r : m));
  return pctLabel(hi.high) !== pctLabel(lo.low) ? { hi, lo } : null;
}

const dayName = (ms: number) => new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });

/** The chart as sentences for a screen reader. */
export function spokenBand(rows: DayRange[]): string {
  const e = extremesOf(rows);
  if (rows.length === 0) return "No readings.";
  const said = (v: number) => `${Math.sign(v) * Math.round(Math.abs(v))} percent`;
  let s = `${rows.length} ${rows.length === 1 ? "day" : "days"} with readings.`;
  if (e) s += ` Lowest ${said(e.lo.low)} on ${dayName(e.lo.day)}; highest ${said(e.hi.high)} on ${dayName(e.hi.day)}.`;
  const k = rows.filter((r) => r.charged).length;
  if (k > 0) s += ` Charged on ${k} ${k === 1 ? "day" : "days"}.`;
  return s;
}

/** A monotone cubic through the points (Fritsch-Carlson): smooth, and never above the highest or below the lowest of
 *  its neighbours, so a smoothed band cannot overshoot a real low or high. */
export function monotoneAt(xs: number[], ys: number[]): (x: number) => number {
  const n = xs.length;
  if (n < 2) return () => ys[0] ?? 0;
  const h = xs.slice(1).map((x, i) => x - xs[i]);
  const m = h.map((d, i) => (ys[i + 1] - ys[i]) / d);
  const t = [m[0], ...m.slice(1).map((v, i) => (m[i] * v <= 0 ? 0 : (2 * m[i] * v) / (m[i] + v))), m[n - 2]];
  return (x) => {
    if (x <= xs[0]) return ys[0];
    if (x >= xs[n - 1]) return ys[n - 1];
    let i = 0;
    while (i < n - 2 && xs[i + 1] <= x) i++;
    const s = (x - xs[i]) / h[i];
    const s2 = s * s;
    const s3 = s2 * s;
    return (2 * s3 - 3 * s2 + 1) * ys[i] + (s3 - 2 * s2 + s) * h[i] * t[i] + (-2 * s3 + 3 * s2) * ys[i + 1] + (s3 - s2) * h[i] * t[i + 1];
  };
}

/** The one stretch of limit to draw: from the last limit change (the last step) to now, or to the end of an earlier
 *  period, carrying that limit. Without any limit stored on the charges (an older server), today's target over the
 *  last two days on the current period only; null for none. A line across the whole period would claim the limit held
 *  all period. */
export function limitSegment(steps: LimitStep[], current: boolean, live: number | null, from: number, now: number): LimitStep | null {
  if (steps.length > 0) return steps[steps.length - 1];
  // (A live target of 100% is no limit worth a line, as on the iPhone; a stored 100% from a charge still is.)
  if (current && validLimit(live) && live < 100) return { x0: Math.max(from, now - 2 * DAY), x1: now, pct: live };
  return null;
}

export function SocTrendChart({
  trend,
  charges,
  domain,
  limit,
}: {
  trend: Pt[];
  charges: ChargeSpan[];
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
    // No period bounds (all time): from the first reading up to now.
    const now = Date.now();
    if (trend.length > 0) return [trend[0].x, Math.max(trend[trend.length - 1].x, now)];
    return [now - DAY, now];
  }, [domain, trend]);
  const rows = useMemo(() => dailyRanges(trend.filter((p) => p.x >= x0 && p.x <= x1), charges), [trend, charges, x0, x1]);
  const iw = w - PAD.l - PAD.r;
  const ih = H - PAD.t - PAD.b;
  const X = (x: number) => PAD.l + ((x - x0) / (x1 - x0)) * iw;
  const Y = (y: number) => PAD.t + ((Y_MAX - y) / Y_MAX) * ih;
  const centre = (r: DayRange) => r.day + DAY / 2;

  // The band and the line, sampled from monotone curves through the days' lows, highs and last levels.
  const shapes = useMemo(() => {
    if (rows.length === 0) return null;
    const cx = rows.map(centre);
    if (rows.length === 1) {
      const half = Math.max(3, ((DAY * 0.3) / (x1 - x0)) * iw);
      return { single: { x: X(cx[0]), half }, band: "", line: "" };
    }
    const lo = monotoneAt(cx, rows.map((r) => r.low));
    const hi = monotoneAt(cx, rows.map((r) => r.high));
    const en = monotoneAt(cx, rows.map((r) => r.end));
    const n = Math.max(8, Math.min(600, Math.round(((cx[cx.length - 1] - cx[0]) / (x1 - x0)) * iw / 2)));
    const xs = Array.from({ length: n + 1 }, (_, i) => cx[0] + ((cx[cx.length - 1] - cx[0]) * i) / n);
    const top = xs.map((x) => `${X(x).toFixed(1)} ${Y(Math.max(hi(x), lo(x) + 0.6)).toFixed(1)}`);
    const bottom = xs.map((x) => `${X(x).toFixed(1)} ${Y(lo(x)).toFixed(1)}`).reverse();
    const line = xs.map((x, i) => `${i === 0 ? "M" : "L"} ${X(x).toFixed(1)} ${Y(en(x)).toFixed(1)}`).join(" ");
    return { single: null, band: `M ${top.join(" L ")} L ${bottom.join(" L ")} Z`, line };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, x0, x1, iw]);

  const extremes = extremesOf(rows);
  // (All time ends at the now of its memo, a moment before this one: a few minutes' slack.)
  const now = Math.min(Date.now(), Math.max(x1, Date.now() - 5 * 60_000));
  const current = now >= x0 && now <= x1;
  const steps = limitSteps(charges, x0, x1, now, limit);
  const seg = limitSegment(steps, current, limit, x0, now);
  // The name goes right of the line's end in the days still to come, or above it when the period has little room left.
  const segTight = seg ? x1 - seg.x1 < 4 * DAY : false;
  const ticks = dayTicks(x0, x1, Math.max(2, Math.min(5, Math.floor(iw / 110))));
  const tickAnchor = (x: number): "start" | "middle" | "end" =>
    X(x) - PAD.l < 14 ? "start" : PAD.l + iw - X(x) < 14 ? "end" : "middle";
  // A name goes on the side with room, and a low near the floor is lifted off the charging ticks.
  const labelRight = (r: DayRange) => centre(r) - x0 < 0.6 * (x1 - x0);

  const dayAt = (x: number): DayRange | null => {
    if (rows.length === 0) return null;
    return rows.reduce((best, r) => (Math.abs(centre(r) - x) < Math.abs(centre(best) - x) ? r : best));
  };
  const hv = hover != null ? dayAt(hover) : null;
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
        aria-label={`Battery level by day. ${spokenBand(rows)}`}
        onPointerMove={move}
        onPointerLeave={() => setHover(null)}
        className="touch-pan-y"
      >
        <rect x={PAD.l} y={Y(LOW_ZONE)} width={iw} height={Y(0) - Y(LOW_ZONE)} fill="#f43f5e" opacity={0.045} data-draw="low-zone" />
        {[0, 50, 100].map((v) => (
          <g key={v}>
            <line x1={PAD.l} x2={PAD.l + iw} y1={Y(v)} y2={Y(v)} className="text-neutral-800" stroke="currentColor" strokeOpacity={0.5} />
            {/* No label on the floor: its "0%" would meet the first day's label in the corner. */}
            {v > 0 && <text x={PAD.l - 6} y={Y(v) + 3.5} textAnchor="end" className="fill-neutral-400" fontSize={CHART_AXIS_PX}>{v}%</text>}
          </g>
        ))}
        {seg && (
          <g data-testid="soc-limit" data-draw="limit">
            <line x1={X(seg.x0)} x2={X(seg.x1)} y1={Y(seg.pct)} y2={Y(seg.pct)} stroke={SOC_LIMIT} strokeOpacity={0.32} strokeWidth={1} data-limit-line />
            {segTight ? (
              <text x={X(seg.x1)} y={Y(seg.pct) - 4} textAnchor="end" className="fill-amber-400 text-[11px]">Limit {Math.round(seg.pct)}%</text>
            ) : (
              <text x={X(seg.x1) + 5} y={Y(seg.pct) + 3.5} textAnchor="start" className="fill-amber-400 text-[11px]">Limit {Math.round(seg.pct)}%</text>
            )}
          </g>
        )}
        {shapes?.single && (
          <rect
            data-draw="band"
            x={shapes.single.x - shapes.single.half}
            y={Y(rows[0].high)}
            width={shapes.single.half * 2}
            height={Math.max(2, Y(rows[0].low) - Y(rows[0].high))}
            rx={3}
            fill={SOC_GREEN}
            fillOpacity={0.22}
          />
        )}
        {shapes && !shapes.single && <path d={shapes.band} data-draw="band" fill={SOC_GREEN} fillOpacity={0.22} stroke={SOC_GREEN} strokeOpacity={0.28} strokeWidth={0.8} strokeLinejoin="round" />}
        {shapes && !shapes.single && <path d={shapes.line} data-draw="line" fill="none" stroke={SOC_GREEN} strokeWidth={1.8} strokeLinejoin="round" strokeLinecap="round" />}
        {rows.filter((r) => r.charged).map((r) => (
          <line key={r.day} data-draw="tick" x1={X(centre(r))} x2={X(centre(r))} y1={Y(0)} y2={Y(0) - 5} stroke={SOC_GREEN} strokeWidth={1.6} strokeLinecap="round" />
        ))}
        {extremes && (
          <g data-draw="extreme">
            {[
              { r: extremes.hi, v: extremes.hi.high, id: "soc-max" },
              { r: extremes.lo, v: extremes.lo.low, id: "soc-min" },
            ].map(({ r, v, id }) => (
              <g key={id}>
                <circle cx={X(centre(r))} cy={Y(v)} r={3} fill="currentColor" className="text-neutral-100" />
                <text
                  x={X(centre(r)) + (labelRight(r) ? 8 : -8)}
                  y={v < 12 ? Y(v) - 9 : Y(v) + 3.5}
                  textAnchor={labelRight(r) ? "start" : "end"}
                  className="fill-neutral-100 text-[11px] font-semibold"
                  data-testid={id}
                >
                  {pctLabel(v)} · {dayName(r.day)}
                </text>
              </g>
            ))}
          </g>
        )}
        {ticks.map((t) => (
          <text key={t.x} x={X(t.x)} y={H - 6} textAnchor={tickAnchor(t.x)} className="fill-neutral-400" fontSize={CHART_AXIS_PX}>{t.label}</text>
        ))}
        {hover != null && hv && (
          <g pointerEvents="none">
            <line x1={X(centre(hv))} x2={X(centre(hv))} y1={PAD.t} y2={Y(0)} stroke="currentColor" className="text-neutral-400" strokeOpacity={0.6} />
            <circle cx={X(centre(hv))} cy={Y(hv.end)} r={3.5} fill="currentColor" className="text-neutral-100" />
          </g>
        )}
      </svg>
      {hover != null && hv && (
        <div
          role="status"
          data-testid="soc-tip"
          className="pointer-events-none absolute top-0 z-10 whitespace-nowrap rounded-md border border-neutral-700 bg-neutral-900/95 px-2 py-1 text-xs text-neutral-200 shadow-lg"
          style={{ left: Math.min(Math.max(X(centre(hv)) - 70, 0), w - 190) }}
        >
          <span>{dayName(hv.day)}</span>
          {" · "}
          <span className="font-medium">{Math.round(hv.low)}–{Math.round(hv.high)}%</span>
          {" · ended "}
          <span className="font-medium">{Math.round(hv.end)}%</span>
          {hv.charged ? " · charged" : ""}
        </div>
      )}
      <p data-soc-caption className="mt-2 text-[11px] text-neutral-400">
        Band: each day&rsquo;s range · line: end of day · ticks: charged
      </p>
    </div>
  );
}
