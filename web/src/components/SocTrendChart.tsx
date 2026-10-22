import { useEffect, useMemo, useRef, useState } from "react";
import { axisDayIndices, mmdd } from "../lib/period";

// Battery (SoC) trend for the ui_theme_next layer, the same chart as the iPhone's (#895): a linear line with a
// fading fill down to 0%, one strip below the 0% line marking charging (emerald) and driving (sky), the charge
// limit as steps from the charges' stored limits (else today's target over the last two days), the high and low labelled inside
// the plot, and a crosshair tooltip.
type Pt = { x: number; y: number };
type Span = { x0: number; x1: number };
// A charge: its span and the limit it was set to (1–100; null or 0 unknown).
type ChargeSpan = Span & { limit?: number | null };
export type LimitStep = { x0: number; x1: number; pct: number };

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

const H = 190;
const PAD = { l: 34, r: 20, t: 8, b: 22 };
// The y domain runs past 0–100% so the strip sits under the 0% line and the high's label fits above 100%.
const Y_MIN = -12;
const Y_MAX = 108;
const STRIP = { lo: -10, hi: -4 };
const DAY = 86_400_000;

/** Integer percent, half away from zero (2.5 → 3). */
export function pctLabel(v: number): string {
  return `${Math.sign(v) * Math.round(Math.abs(v))}%`;
}

/** The x-axis days, labelled as Miles per day (axisDayIndices: a month on the 1st, 11th, 21st and its last day,
 *  MM-DD); longer spans, up to `n` evenly spaced dates. */
export function dayTicks(x0: number, x1: number, n: number): { x: number; label: string }[] {
  if (x1 - x0 <= 32 * DAY) {
    const days: Date[] = [];
    const d = new Date(x0);
    d.setHours(0, 0, 0, 0);
    if (d.getTime() < x0) d.setDate(d.getDate() + 1);
    for (; d.getTime() < x1; d.setDate(d.getDate() + 1)) days.push(new Date(d));
    const picked = axisDayIndices(days);
    if (picked) return picked.map((i) => ({ x: days[i].getTime(), label: mmdd(days[i]) }));
  }
  const fmt = (ms: number) => new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return Array.from({ length: n }, (_, i) => x0 + ((x1 - x0) * i) / (n - 1)).map((x) => ({ x, label: fmt(x) }));
}

export function SocTrendChart({
  trend,
  charges,
  drives,
  domain,
  limit,
}: {
  trend: Pt[];
  charges: ChargeSpan[];
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
    // No period bounds (all time): from the first reading up to now.
    const now = Date.now();
    if (trend.length > 0) return [trend[0].x, Math.max(trend[trend.length - 1].x, now)];
    return [now - DAY, now];
  }, [domain, trend]);
  const pts = useMemo(() => trend.filter((p) => p.x >= x0 && p.x <= x1), [trend, x0, x1]);
  const iw = w - PAD.l - PAD.r;
  const ih = H - PAD.t - PAD.b;
  const X = (x: number) => PAD.l + ((x - x0) / (x1 - x0)) * iw;
  const Y = (y: number) => PAD.t + ((Y_MAX - y) / (Y_MAX - Y_MIN)) * ih;
  const xy = pts.map((p) => [X(p.x), Y(p.y)] as [number, number]);
  const line = xy.length > 0 ? xy.map(([x, y], i) => `${i === 0 ? "M" : "L"} ${x.toFixed(1)} ${y.toFixed(1)}`).join(" ") : "";
  const area = xy.length > 1 ? `${line} L ${xy[xy.length - 1][0].toFixed(1)} ${Y(0)} L ${xy[0][0].toFixed(1)} ${Y(0)} Z` : "";
  const clip = (s: Span) => ({ a: Math.max(s.x0, x0), b: Math.min(s.x1, x1) });
  // One strip under the 0% line, no wash over the plot. A stretch is at least 1% of the width (and 3 px) so a
  // short drive still shows.
  const band = (s: Span, i: number, color: string, kind: string) => {
    const { a, b } = clip(s);
    if (b <= a) return null;
    const wd = Math.max(3, iw * 0.01, X(b) - X(a));
    return (
      <g key={`${kind}${i}`} data-band={kind} data-draw={kind}>
        <rect x={X(a)} y={Y(STRIP.hi)} width={wd} height={Y(STRIP.lo) - Y(STRIP.hi)} fill={color} />
      </g>
    );
  };
  const drawnCharge = charges.some((s) => clip(s).b > clip(s).a);
  const drawnDrive = drives.some((s) => clip(s).b > clip(s).a);
  const lo = pts.length >= 2 ? pts.reduce<Pt | null>((m, p) => (m == null || p.y < m.y ? p : m), null) : null;
  const hi = pts.length >= 2 ? pts.reduce<Pt | null>((m, p) => (m == null || p.y > m.y ? p : m), null) : null;
  const extremes = hi && lo && pctLabel(hi.y) !== pctLabel(lo.y) ? { hi, lo } : null;
  // (All time ends at the now of its memo, a moment before this one: a few minutes' slack.)
  const now = Math.min(Date.now(), Math.max(x1, Date.now() - 5 * 60_000));
  const current = now >= x0 && now <= x1;
  // The limit as steps from the charges' stored limits (limitSteps). Without any (an older server, or none known
  // in the period), today's target over the last two days, on the current period only, as the iPhone (#895).
  const steps = limitSteps(charges, x0, x1, now, limit);
  const limitLine: LimitStep[] =
    steps.length > 0 ? steps : current && validLimit(limit) ? [{ x0: Math.max(x0, now - 2 * DAY), x1: now, pct: limit }] : [];
  const limitShown = limitLine.length > 0;
  let limitPath = "";
  limitLine.forEach((s, i) => {
    const prev = limitLine[i - 1];
    limitPath += prev && prev.x1 === s.x0 ? ` V ${Y(s.pct).toFixed(1)}` : ` M ${X(s.x0).toFixed(1)} ${Y(s.pct).toFixed(1)}`;
    limitPath += ` H ${X(s.x1).toFixed(1)}`;
  });
  // One value, on the current period: at the line's end at now, right of it in the days still to come, or above it
  // in a period's last four days (no room to the right). Past periods carry no value.
  const limitEnd = limitLine[limitLine.length - 1];
  const limitLabel = current && limitEnd && now - limitEnd.x1 < 60_000 ? limitEnd : null;
  const limitLabelAbove = x1 - now < 4 * DAY;
  const ticks = dayTicks(x0, x1, Math.max(2, Math.min(5, Math.floor(iw / 110))));
  const tickAnchor = (x: number): "start" | "middle" | "end" =>
    X(x) - PAD.l < 14 ? "start" : PAD.l + iw - X(x) < 14 ? "end" : "middle";

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
        aria-label="Battery state of charge over time, with charging and driving marked below the line"
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
        {area && <path d={area} fill="url(#soc-fill)" className="text-neutral-100" />}
        {line && <path d={line} data-draw="line" fill="none" stroke="currentColor" className="text-neutral-100" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />}
        {limitShown && (
          <g data-testid="soc-limit" data-draw="limit" data-steps={steps.length}>
            <path d={limitPath.trim()} data-limit-line fill="none" stroke="#f59e0b" strokeDasharray="4 3" strokeWidth={1.5} />
            {limitLabel &&
              (limitLabelAbove ? (
                <text x={X(limitLabel.x1)} y={Y(limitLabel.pct) - 5} textAnchor={X(limitLabel.x1) - PAD.l < 80 ? "start" : "end"} className="fill-amber-400 text-[10px]">Limit {Math.round(limitLabel.pct)}%</text>
              ) : (
                <text x={X(limitLabel.x1) + 4} y={Y(limitLabel.pct) + 3.5} textAnchor="start" className="fill-amber-400 text-[10px]">Limit {Math.round(limitLabel.pct)}%</text>
              ))}
          </g>
        )}
        {extremes && (
          <g data-draw="extreme">
            <circle cx={X(extremes.hi.x)} cy={Y(extremes.hi.y)} r={3.5} fill="currentColor" stroke="currentColor" className="text-neutral-100" />
            <text x={X(extremes.hi.x)} y={Y(extremes.hi.y) - 7} textAnchor="middle" className="fill-neutral-100 text-[10px]" data-testid="soc-max">{pctLabel(extremes.hi.y)}</text>
            <circle cx={X(extremes.lo.x)} cy={Y(extremes.lo.y)} r={3.5} fill="currentColor" stroke="currentColor" className="text-neutral-100" />
            <text x={X(extremes.lo.x) + 7} y={Y(extremes.lo.y) + 3.5} textAnchor="start" className="fill-neutral-100 text-[10px]" data-testid="soc-min">{pctLabel(extremes.lo.y)}</text>
          </g>
        )}
        {ticks.map((t) => (
          <text key={t.x} x={X(t.x)} y={H - 6} textAnchor={tickAnchor(t.x)} className="fill-neutral-500 text-[10px]">{t.label}</text>
        ))}
        {hover != null && hv && (
          <g pointerEvents="none">
            <line x1={X(hover)} x2={X(hover)} y1={PAD.t} y2={Y(0)} stroke="currentColor" className="text-neutral-400" strokeOpacity={0.6} />
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
        {limitShown && (
          <span data-legend="limit">
            <svg aria-hidden width="16" height="4" className="mr-1 inline-block align-middle">
              <line data-testid="legend-limit-swatch" x1="0" x2="16" y1="2" y2="2" stroke="#f59e0b" strokeWidth="1.5" strokeDasharray="4 3" />
            </svg>
            {steps.length > 0 ? "Charge limit" : "Charge limit now"}
          </span>
        )}
        {extremes && (
          <span data-legend="extreme">
            <span aria-hidden className="mr-1 inline-block h-2 w-2 rounded-full bg-neutral-100 align-middle" />
            High and low
          </span>
        )}
      </p>
    </div>
  );
}
