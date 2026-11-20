import { useQuery } from "@tanstack/react-query";
import { backend, type ParkedSeries } from "../lib/api";
import { useIsLight } from "../lib/vehicleImage";
import { bandDuration, parkedDrawable, parkedText, parkedZone, type ParkedFigure } from "../lib/parkedSeries";
import { GraphHead, HaloDot, Legend, Pill, SoftDefs, SoftLine, labelWidth, softPath, useBox, useSoftId, useSoftInk, type Pt } from "./NowGraphKit";

// The Now card's "Battery while parked" graph (design_preview "now_parked_graph"), for the parked, plugged and asleep states: the last 24 hours
// of the battery as one soft line that breaks where the truck drove or charged, each sleep as a soft indigo span with its length, a small amber
// dot at each wake-up, the start and current values, and "At this rate" once the truck has been parked a few hours. The numbers are the
// server's (GET /parked-series); the iPhone draws the same. Text uses the app's own sizes.

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

export function ParkedGraph({ series, unit }: { series: ParkedSeries; unit: "mi" | "km" }) {
  const [ref, { w, h }] = useBox();
  const light = useIsLight();
  const ink = useSoftInk();
  const id = useSoftId();
  const text = parkedText(series, unit);
  // The chart's text is in rem, so at a larger text setting its labels need more room: the optional ones give way, never overlap.
  const scale = (typeof document !== "undefined" ? parseFloat(getComputedStyle(document.documentElement).fontSize) || 16 : 16) / 16;
  const t0 = ms(series.from);
  const t1 = ms(series.to);
  const pts = series.points;
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

  // One path per run of readings (a drive or a charge between them breaks the line).
  const runs: Pt[][] = [];
  let seg = -1;
  for (const p of pts) {
    if (p.seg !== seg) {
      runs.push([]);
      seg = p.seg;
    }
    runs[runs.length - 1].push({ x: sx(ms(p.t)), y: sy(p.soc) });
  }
  const lx = sx(ms(last.t));
  const ly = sy(last.soc);
  const fx = sx(ms(first.t));
  const fy = sy(first.soc);
  const parkedX = series.parked_since ? sx(ms(series.parked_since)) : null;
  const valueLeft = lx - PAD.l > 60;
  // The "Parked 5:20 PM" pill needs room clear of the start value and the edges; the header says the same time when it is left out.
  const pillW = labelWidth(text.parkedAt ?? "");
  const pill = scale <= 1.25 && parkedX != null && parkedX - pillW / 2 > fx + 56 && parkedX + pillW / 2 < w - PAD.r ? text.parkedAt : null;
  const showMid = w >= 240 * scale;
  const ariaLabel = `Battery while parked, ${Math.round(first.soc)}% 24 hours ago to ${Math.round(last.soc)}% now${text.change ? `, ${text.change.value.replace("−", "minus ")} ${text.change.since}` : ""}, ${series.sleeps.length} ${series.sleeps.length === 1 ? "sleep" : "sleeps"}, ${series.wakes.length} ${series.wakes.length === 1 ? "wake-up" : "wake-ups"}`;

  return (
    <div data-testid="now-parked-graph" className="flex min-w-0 flex-col min-[900px]:h-full">
      <GraphHead title="Battery while parked" change={text.change?.value ?? null} since={text.change?.since} testid="now-parked-change" />
      <div ref={ref} className="h-[12rem] min-w-0 min-[900px]:h-auto min-[900px]:min-h-[14rem] min-[900px]:flex-1">
        {w > 0 && h > 0 && (
          <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} role="img" aria-label={ariaLabel} className="block overflow-visible">
            <SoftDefs id={id} ink={ink} />
            <defs>
              <linearGradient id={`${id}-sleep`} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" stopColor={ink.band} stopOpacity={light ? 0.22 : 0.34} />
                <stop offset="1" stopColor={ink.band} stopOpacity={light ? 0.04 : 0.05} />
              </linearGradient>
            </defs>
            {series.sleeps.map((s, i) => {
              const a = sx(Math.max(ms(s.from), t0));
              const b = sx(Math.min(ms(s.to), t1));
              const sw = Math.max(b - a, 0);
              if (sw < 2) return null;
              const topY = sy(sleepTop(pts, ms(s.from), ms(s.to), t0, t1) ?? last.soc);
              const dur = (Math.min(ms(s.to), t1) - Math.max(ms(s.from), t0)) / 1000;
              return (
                <g key={`${s.from}-${i}`} data-testid="now-parked-sleep">
                  <rect x={a + 1} y={topY} width={Math.max(sw - 2, 1)} height={Math.max(base - BAR - 3 - topY, 0)} rx={8} fill={`url(#${id}-sleep)`} />
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
              <line x1={parkedX} x2={parkedX} y1={PAD.t - 4} y2={base - BAR - 3} stroke="currentColor" strokeOpacity=".18" className="text-neutral-400" />
            )}
            {runs.map((r, i) => (r.length > 1 ? <SoftLine key={i} id={id} d={softPath(r)} color={ink.line} width={3} /> : null))}
            {series.wakes.map((k) => (
              <HaloDot key={k.t} id={id} x={sx(ms(k.t))} y={sy(k.soc_after)} color={amber} />
            ))}
            <HaloDot id={id} x={lx} y={ly} color={ink.line} big />
            {pill && parkedX != null && <Pill x={parkedX} y={12} text={pill} w={pillW} />}
            <text x={fx + 2} y={Math.max(fy - 14, 12)} className="fill-neutral-300 text-xs">
              {`${first.soc.toFixed(1)}%`}
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

// The battery at the start of a sleep (the line's height where its span begins), so the span hangs from the line.
function sleepTop(pts: ParkedSeries["points"], from: number, to: number, t0: number, t1: number): number | null {
  const a = Math.max(from, t0);
  const b = Math.min(to, t1);
  let best: number | null = null;
  for (const p of pts) {
    const t = ms(p.t);
    if (t >= a && t <= b) best = best == null ? p.soc : Math.max(best, p.soc);
  }
  if (best != null) return best;
  let prev: number | null = null;
  for (const p of pts) if (ms(p.t) <= a) prev = p.soc;
  return prev;
}

// The phone's three tiles, under the graph; from 900 px the same figures are rows beside the ring (ParkedRows).
function TilesRow({ figures }: { figures: ParkedFigure[] }) {
  if (figures.length === 0) return null;
  return (
    <div data-testid="now-parked-tiles" className="mt-3 grid grid-cols-[repeat(auto-fit,minmax(6.5rem,1fr))] gap-2 min-[900px]:hidden max-[359px]:grid-cols-1">
      {figures.map((f) => (
        <div key={f.id} data-testid={`now-parked-tile-${f.id}`} className="min-w-0 rounded-2xl bg-neutral-800/50 px-3 py-2.5">
          <p className="text-sm font-semibold tabular-nums text-neutral-100">{f.value}</p>
          <p className="text-xs text-neutral-400">{f.note || f.label}</p>
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
