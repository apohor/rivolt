import { createContext, useContext, type ReactNode } from "react";
import { chargeFinishMs, clock } from "../lib/nowState";
import { powerMax, useOpenChargeSeries } from "./LiveChargeGraph";
import { GraphHead, HaloDot, Legend, Pill, QuietLine, SoftDefs, SoftFill, SoftLine, labelWidth, softPath, thin, useBox, useSoftId, useSoftInk, type Pt } from "./NowGraphKit";

// The Now card's charging graph (design_preview "now_charge_graph"), in the soft look of the parked graph: the open charge's battery from plug-in
// to now as one soft line, the charger's power as a quieter second line, and, when the card has a finish time, the way on to the target as a
// soft dotted continuation with the finish time in a small pill. The series are the Live page's (stored samples, no Rivian call), read at
// its 30 s pace. Under two minutes of samples it says so instead of drawing a stub.
const MIN_SPAN_MS = 120_000;
// The estimate's width, relative to the session so far.
const EST_SHARE = 0.8;
const PAD = { t: 30, b: 26, l: 6, r: 10 };

// The open charge's series, read once for the card: the graph draws it and the temperatures row takes the pack's reading from it.
const SeriesContext = createContext<ReturnType<typeof useOpenChargeSeries> | null>(null);

export function ChargeSeriesProvider({ vehicleId, enabled, children }: { vehicleId: string; enabled: boolean; children: ReactNode }) {
  return <SeriesContext.Provider value={useOpenChargeSeries(vehicleId, enabled)}>{children}</SeriesContext.Provider>;
}

// Whether the charge's series is still on its way (the card keeps its height until it has answered).
export function useChargePending(): boolean {
  return useContext(SeriesContext)?.pending ?? false;
}

// The pack's temperature from the newest stored sample, only while it is recent (a reading older than this is not shown).
const PACK_FRESH_MS = 15 * 60_000;
export function usePackTemp(nowMs: number): number | null {
  const pack = useContext(SeriesContext)?.pack;
  return pack && nowMs - pack.at <= PACK_FRESH_MS ? pack.avg : null;
}

export function NowChargeGraph({ nowMs, nowPct, targetPct, etaHours, powerKw = null }: { nowMs: number; nowPct: number; targetPct: number | null; etaHours: number | null; powerKw?: number | null }) {
  const series = useContext(SeriesContext);
  const { open, soc, power, startPct } = series ?? { open: undefined, soc: [], power: [], startPct: null };
  const [ref, { w, h }] = useBox();
  const ink = useSoftInk();
  const id = useSoftId();
  if (!open) return null;
  const first = soc[0];
  const last = soc[soc.length - 1];
  if (!first || !last || soc.length < 3 || last.x - first.x < MIN_SPAN_MS) {
    return (
      <p data-testid="now-charge-graph-collecting" className="text-xs text-neutral-400">
        Collecting this charge's first minutes. The graph appears shortly.
      </p>
    );
  }
  const target = targetPct != null && targetPct > nowPct ? targetPct : null;
  // The pill's clock is the chip's and the slot's (chargeFinishMs: the shown whole minutes from now, never from the last stored sample).
  const endX = target != null && etaHours != null && etaHours > 0 ? chargeFinishMs(etaHours, nowMs) : null;
  const peak = power.reduce((m, p) => Math.max(m, p.y), 0);
  // The power named is the card's live figure (the slot's big number), so one kW is said once; the line itself is the stored samples.
  const lastPower = power[power.length - 1];
  const nowKw = powerKw ?? lastPower?.y ?? null;
  // The two numbers the graph shows are the first stored reading and the card's own battery figure (the ring), so the change is exactly their difference.
  const startShown = Math.round(startPct ?? first.y);
  const nowShown = Math.round(nowPct);
  const gained = nowShown - startShown;

  // The estimate takes at most this share of the width again as the session so far, so a slow charge with hours left still shows its start readably
  // (the dotted way on is then not to scale; the pill carries the finish time).
  const x1 = endX != null ? last.x + Math.min(endX - last.x, (last.x - first.x) * EST_SHARE) : last.x;
  const hi = Math.max(last.y, target ?? 0, ...soc.map((p) => p.y));
  const lo = Math.min(...soc.map((p) => p.y));
  const span = Math.max(hi - lo, 4);
  const yLo = lo - span * 0.12;
  const base = h - PAD.b;
  const sx = (x: number) => PAD.l + ((x - first.x) / (x1 - first.x)) * (w - PAD.l - PAD.r);
  const sy = (v: number) => PAD.t + (1 - (v - yLo) / (hi - yLo)) * (base - PAD.t);
  const sp = (v: number) => base - (v / powerMax(peak)) * (base - PAD.t);
  const socPts: Pt[] = thin(soc).map((p) => ({ x: sx(p.x), y: sy(p.y) }));
  const powPts: Pt[] = thin(power).map((p) => ({ x: sx(p.x), y: sp(p.y) }));
  const line = softPath(socPts);
  const lx = sx(last.x);
  const ly = sy(last.y);
  const ex = endX != null ? sx(x1) : null;
  const ey = target != null ? sy(target) : null;
  // The estimate leaves "now" as a curve: it starts with the slope the battery line had over its last stretch (the last ~24 px or so; never steeper than the way to the
  // target) and eases in level at the target, so there is no angle at the "now" dot. It still ends at the real finish time.
  const estimate = (() => {
    if (ex == null || ey == null) return "";
    const dx = ex - lx;
    const dy = ey - ly;
    const from = socPts.find((p) => p.x >= lx - Math.max(24, 0.08 * (lx - socPts[0].x))) ?? socPts[0];
    const m = lx - from.x > 1 ? (ly - from.y) / (lx - from.x) : 0;
    const rise = Math.min(0, Math.max(dy, m * dx * 0.4));
    const f = (v: number) => v.toFixed(1);
    return `M${f(lx)} ${f(ly)} C${f(lx + dx * 0.4)} ${f(ly + rise)} ${f(ex - dx * 0.35)} ${f(ey)} ${f(ex)} ${f(ey)}`;
  })();
  const pill = target != null ? `${Math.round(target)}% · ${clock(endX ?? last.x)}` : null;
  const startText = w >= 260 ? `Plugged in ${clock(first.x)}` : clock(first.x);
  const labelNow = lx - PAD.l > 170;
  const labelStartValue = lx - PAD.l > 90;
  const valueLeft = lx - PAD.l > 60;

  return (
    <div data-testid="now-charge-graph" className="flex min-w-0 flex-col min-[900px]:h-full">
      <GraphHead title="Battery while charging" change={gained > 0 ? `+${gained}%` : null} since={`since ${clock(first.x)}`} />
      <div ref={ref} className="h-[170px] min-w-0 min-[900px]:h-auto min-[900px]:min-h-[200px] min-[900px]:flex-1">
        {w > 0 && h > 0 && (
          <svg
            width={w}
            height={h}
            viewBox={`0 0 ${w} ${h}`}
            role="img"
            aria-label={`Battery from ${startShown}% to ${nowShown}% since ${clock(first.x)}${nowKw != null ? `, charging at ${Math.round(nowKw)} kilowatts` : ""}${pill ? `, reaching ${pill.replace("·", "at")}` : ""}`}
            className="block overflow-visible"
          >
            <SoftDefs id={id} ink={ink} />
            {ex != null && <rect x={lx + 12} y={PAD.t + 4} width={Math.max(ex - lx - 20, 4)} height={base - PAD.t - 4} rx={28} fill={`url(#${id}-band)`} filter={`url(#${id}-soften)`} />}
            <SoftFill id={id} d={line} baseY={base} x0={socPts[0].x} x1={lx} />
            {powPts.length > 1 && <QuietLine d={softPath(powPts)} color={ink.quiet} />}
            <SoftLine id={id} d={line} color={ink.line} />
            {ex != null && ey != null && (
              <>
                <path d={estimate} data-testid="now-charge-graph-estimate-line" fill="none" stroke={ink.line} strokeWidth={2.2} strokeLinecap="round" strokeDasharray="0.1 7" strokeOpacity=".8" />
                <HaloDot id={id} x={ex} y={ey} color={ink.line} />
                <Pill x={w - PAD.r} y={Math.max(ey - 18, 12)} text={pill ?? ""} w={labelWidth(pill ?? "")} anchor="end" />
              </>
            )}
            <HaloDot id={id} x={socPts[0].x} y={socPts[0].y} color={ink.line} />
            <HaloDot id={id} x={lx} y={ly} color={ink.line} big />
            {labelStartValue && (
              <text x={socPts[0].x + 2} y={Math.max(socPts[0].y - 14, 12)} className="fill-neutral-300 text-xs">
                {startShown}%
              </text>
            )}
            <text x={valueLeft ? lx - 10 : lx + 10} y={Math.max(ly - 12, 12)} textAnchor={valueLeft ? "end" : "start"} className="fill-neutral-100 text-sm font-semibold">
              {nowShown}%
            </text>
            <text x={PAD.l} y={h - 6} className="fill-neutral-500 text-xs">
              {startText}
            </text>
            {labelNow && (
              <text x={lx} y={h - 6} textAnchor="middle" className="fill-neutral-500 text-xs">
                now
              </text>
            )}
          </svg>
        )}
      </div>
      <Legend
        items={[
          { swatch: "line", color: ink.line, label: "Battery" },
          ...(nowKw != null ? [{ swatch: "line" as const, color: ink.quiet, label: `Power ${nowKw.toFixed(nowKw < 10 ? 1 : 0)} kW now` }] : []),
          ...(endX != null ? [{ swatch: "dash" as const, color: ink.line, label: "Estimate", testid: "now-charge-graph-estimate" }] : []),
        ]}
      />
    </div>
  );
}
