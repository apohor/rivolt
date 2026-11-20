import { useQuery } from "@tanstack/react-query";
import { backend, type ParkedSeries } from "../lib/api";
import { clock } from "../lib/nowState";
import { useIsLight } from "../lib/vehicleImage";
import { bandDuration, parkedAtLabel, parkedDrawable, parkedChange, parkedText, parkedZone, type ParkedFigure } from "../lib/parkedSeries";
import { GraphHead, HaloDot, Legend, Pill, SoftDefs, SoftLine, labelWidth, easeSteps, softPath, useBox, useSoftId, useSoftInk, type Pt } from "./NowGraphKit";

// The Now card's "Battery while parked" graph (design_preview "now_parked_graph"), for the parked, plugged and asleep states: the last 24 hours
// of the battery as one continuous soft line (a drive or charge between readings is bridged, not split), each sleep as a soft indigo fill under
// the line with its length, a small amber dot at each wake-up, the start and current values, and "At this rate" once the truck has been parked
// a few hours. The line ends at the card's own battery (the ring) and starts at the first stored reading; the header change is their
// difference. The figures are the server's (GET /parked-series); the iPhone draws the same. Text uses the app's own sizes.

const PAD = { t: 34, b: 26, l: 6, r: 10 };
const BAR = 5;
const LABEL_MIN_W = 58;

// The series for the card; null while loading, absent, or without a line to draw (the graph then shows nothing and the card is as it was).
export function useParkedSeries(vehicleID: string, enabled: boolean): ParkedSeries | null {
  const q = useQuery({
    queryKey: ["parked-series", vehicleID],
    queryFn: () => backend.vehicleParkedSeries(vehicleID, parkedZone()),
    enabled,
    refetchInterval: 60_000,
    retry: 0,
  });
  return enabled && parkedDrawable(q.data) ? q.data : null;
}

const ms = (iso: string) => Date.parse(iso);

// The chart's text is in rem, so at a larger text setting its labels need more room: the optional ones give way, never overlap.
function useTextScale(): number {
  return (typeof document !== "undefined" ? parseFloat(getComputedStyle(document.documentElement).fontSize) || 16 : 16) / 16;
}

export function ParkedGraph({ series, unit, nowPct }: { series: ParkedSeries; unit: "mi" | "km"; nowPct: number | null }) {
  const [ref, { w, h }] = useBox();
  const light = useIsLight();
  const ink = useSoftInk();
  const id = useSoftId();
  const text = parkedText(series, unit);
  const scale = useTextScale();
  const t0 = ms(series.from);
  const t1 = ms(series.to);
  // The card's battery is the end of the line: the stored readings, then the ring's value at now.
  const stored = series.points;
  const pts = nowPct != null && nowPct > 0 ? [...stored.filter((p) => t1 - ms(p.t) > 60_000), { t: series.to, soc: nowPct, seg: stored[stored.length - 1].seg }] : stored;
  const first = pts[0];
  const last = pts[pts.length - 1];
  const hi = Math.max(...pts.map((p) => p.soc));
  const lo = Math.min(...pts.map((p) => p.soc));
  const span = Math.max(hi - lo, 4);
  // The line rides the upper half so the sleep spans and their labels sit under it.
  const yHi = hi + span * 0.15;
  const yLo = lo - span * 1.1;
  const base = h - PAD.b;
  const sx = (t: number) => PAD.l + ((t - t0) / (t1 - t0)) * (w - PAD.l - PAD.r);
  const sy = (v: number) => PAD.t + (1 - (v - yLo) / (yHi - yLo)) * (base - PAD.t);
  const bar = light ? "#6366f1" : "#a5b4fc";
  const amber = ink.warm;

  const wakeAt = new Set(series.wakes.map((k) => ms(k.t)));
  const markerPt = pts.find((p) => p.seg === last.seg);
  // The readings that carry a dot or a label stay on the line when it is eased: the wake-ups, the first and last, and the parked marker's.
  const line: Pt[] = pts.map((p, i) => ({ x: sx(ms(p.t)), y: sy(p.soc), pin: i === 0 || i === pts.length - 1 || wakeAt.has(ms(p.t)) || p === markerPt }));
  const lx = sx(ms(last.t));
  const ly = sy(last.soc);
  const fx = sx(ms(first.t));
  const parkedMs = series.parked_since ? ms(series.parked_since) : null;
  const parkedX = parkedMs != null && parkedMs > t0 && parkedMs < t1 ? sx(parkedMs) : null;
  const parkedLabel = parkedMs != null ? parkedAtLabel(parkedMs) : null;
  // Only readings from the parked marker on count toward the change: a drive or a charge earlier in the window is not "while parked".
  const curSeg = pts.filter((p) => p.seg === last.seg);
  const sinceIn = parkedMs != null && parkedMs > t0 ? parkedMs : null;
  // The start label sits on the reading the header measures from: the parked marker's, else the window's first.
  const start = sinceIn != null ? curSeg[0] : first;
  const change = parkedChange(start.soc, last.soc, sinceIn);
  const sleepBase = base - BAR - 3;
  const linePath = softPath(easeSteps(line));
  const valueLeft = lx - PAD.l > 60;
  // The "Parked 5:20 PM" pill needs room clear of the start value and the edges; the header says the same time when it is left out.
  const pillW = labelWidth(parkedLabel ?? "");
  const pill = scale <= 1.25 && parkedX != null && parkedX - pillW / 2 > fx + 56 && parkedX + pillW / 2 < w - PAD.r ? parkedLabel : null;
  const showMid = w >= 240 * scale;
  const ariaLabel = `Battery while parked, ${Math.round(start.soc)}% ${sinceIn != null ? `at ${clock(sinceIn)}` : "24 hours ago"} to ${Math.round(last.soc)}% now, ${change.value.replace("−", "minus ")} ${change.since}, ${series.sleeps.length} ${series.sleeps.length === 1 ? "sleep" : "sleeps"}, ${series.wakes.length} ${series.wakes.length === 1 ? "wake-up" : "wake-ups"}`;

  return (
    <div data-testid="now-parked-graph" className="flex min-w-0 flex-col min-[900px]:h-full">
      <GraphHead title="Battery while parked" change={change.value} since={change.since} testid="now-parked-change" />
      <div ref={ref} className="h-[12rem] min-w-0 min-[900px]:h-auto min-[900px]:min-h-[14rem] min-[900px]:flex-1">
        {w > 0 && h > 0 && (
          <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} role="img" aria-label={ariaLabel} className="block overflow-visible">
            <SoftDefs id={id} ink={ink} />
            <defs>
              <linearGradient id={`${id}-sleep`} gradientUnits="userSpaceOnUse" x1="0" y1={PAD.t} x2="0" y2={sleepBase}>
                <stop offset="0" stopColor={ink.band} stopOpacity={light ? 0.22 : 0.34} />
                <stop offset="1" stopColor={ink.band} stopOpacity={light ? 0.04 : 0.05} />
              </linearGradient>
            </defs>
            {series.sleeps.map((sl, i) => {
              const a = sx(Math.max(ms(sl.from), t0));
              const b = sx(Math.min(ms(sl.to), t1));
              const sw = Math.max(b - a, 0);
              if (sw < 2) return null;
              const dur = (Math.min(ms(sl.to), t1) - Math.max(ms(sl.from), t0)) / 1000;
              return (
                <g key={`${sl.from}-${i}`} data-testid="now-parked-sleep">
                  <clipPath id={`${id}-sl${i}`}>
                    <rect x={a + 1} y={0} width={Math.max(sw - 2, 1)} height={sleepBase} rx={10} />
                  </clipPath>
                  <path d={`${linePath} L${lx.toFixed(1)} ${sleepBase} L${fx.toFixed(1)} ${sleepBase} Z`} fill={`url(#${id}-sleep)`} clipPath={`url(#${id}-sl${i})`} />
                  <rect x={a + 1} y={base - BAR} width={Math.max(sw - 2, 1)} height={BAR} rx={BAR / 2} fill={bar} fillOpacity=".75" />
                  {sw > LABEL_MIN_W * scale && (
                    <text x={a + sw / 2} y={base - BAR - 10} textAnchor="middle" className="fill-indigo-300 text-xs">
                      {`☾ ${bandDuration(dur)}`}
                    </text>
                  )}
                </g>
              );
            })}
            {parkedX != null && parkedX > PAD.l && (
              <line x1={parkedX} x2={parkedX} y1={PAD.t - 4} y2={sleepBase} stroke="currentColor" strokeOpacity=".18" className="text-neutral-400" />
            )}
            <SoftLine id={id} d={linePath} color={ink.line} width={3} />
            {series.wakes.map((k) => (
              <HaloDot key={k.t} id={id} x={sx(ms(k.t))} y={sy(k.soc_after)} color={amber} />
            ))}
            <HaloDot id={id} x={lx} y={ly} color={ink.line} big />
            {pill && parkedX != null && <Pill x={parkedX} y={12} text={pill} w={pillW} />}
            <text x={sx(ms(start.t)) + (sinceIn != null && lx - sx(ms(start.t)) < 90 ? -4 : 2)} textAnchor={sinceIn != null && lx - sx(ms(start.t)) < 90 ? "end" : "start"} y={sinceIn != null ? sy(start.soc) + 20 : Math.max(sy(start.soc) - 14, 12)} className="fill-neutral-300 text-xs">
              {`${start.soc.toFixed(1)}%`}
            </text>
            <text x={valueLeft ? lx - 10 : lx + 10} y={ly + 22} textAnchor={valueLeft ? "end" : "start"} className="fill-neutral-100 text-sm font-semibold">
              {`${last.soc.toFixed(1)}%`}
            </text>
            <text x={PAD.l} y={h - 6} className="fill-neutral-500 text-xs">
              24 h ago
            </text>
            {showMid && (
              <text x={(PAD.l + w - PAD.r) / 2} y={h - 6} textAnchor="middle" className="fill-neutral-500 text-xs">
                12 h
              </text>
            )}
            <text x={w - PAD.r} y={h - 6} textAnchor="end" className="fill-neutral-500 text-xs">
              now
            </text>
          </svg>
        )}
      </div>
      <Legend
        items={[
          ...(series.sleeps.length > 0 ? [{ swatch: "dot" as const, color: bar, label: "asleep" }] : []),
          ...(series.wakes.length > 0 ? [{ swatch: "dot" as const, color: amber, label: "woke up" }] : []),
        ]}
      />
      <TilesRow figures={text.figures} />
      {text.projection && (
        <p data-testid="now-parked-projection" className="mt-3 flex items-start gap-2 text-sm text-neutral-400">
          <i aria-hidden className="mt-2 inline-block h-0.5 w-3.5 shrink-0 rounded" style={{ background: ink.line, opacity: 0.6 }} />
          {text.projection}
        </p>
      )}
    </div>
  );
}

// The phone's three tiles, under the graph; from 900 px the same figures are rows beside the ring (ParkedRows).
function TilesRow({ figures }: { figures: ParkedFigure[] }) {
  const scale = useTextScale();
  if (figures.length === 0) return null;
  return (
    <div data-testid="now-parked-tiles" className={`mt-3 grid gap-2 min-[900px]:hidden max-[359px]:grid-cols-1 ${scale > 1.25 ? "grid-cols-1" : "grid-cols-3"}`}>
      {figures.map((f) => (
        <div key={f.id} data-testid={`now-parked-tile-${f.id}`} className="min-w-0 rounded-2xl bg-neutral-800/50 px-2.5 py-2.5">
          <p className="text-sm font-semibold tabular-nums text-neutral-100">{f.value}</p>
          <p className="text-xs text-neutral-400">{f.tileNote || f.label}</p>
        </div>
      ))}
    </div>
  );
}

// The desktop's rows (from 900 px), in the left half under the ring and range.
export function ParkedRows({ series, unit }: { series: ParkedSeries; unit: "mi" | "km" }) {
  const figures = parkedText(series, unit).figures;
  if (figures.length === 0) return null;
  return (
    <dl data-testid="now-parked-rows" className="hidden divide-y divide-neutral-800/60 min-[900px]:block">
      {figures.map((f) => (
        <div key={f.id} data-testid={`now-parked-row-${f.id}`} className="flex items-baseline justify-between gap-3 py-2">
          <dt className="text-sm text-neutral-400">{f.rowLabel}</dt>
          <dd className="text-right text-sm font-semibold tabular-nums text-neutral-100">
            {f.rowValue}
            {f.note && f.id !== "asleep" && <span className="ml-1.5 text-xs font-normal text-neutral-400">{f.note}</span>}
          </dd>
        </div>
      ))}
    </dl>
  );
}
