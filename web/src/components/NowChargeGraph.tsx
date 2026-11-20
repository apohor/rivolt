import { clock } from "../lib/nowState";
import { powerMax, useOpenChargeSeries } from "./LiveChargeGraph";
import { GraphHead, HaloDot, Legend, Pill, QuietLine, SoftDefs, SoftFill, SoftLine, labelWidth, softPath, thin, useBox, useSoftId, useSoftInk, type Pt } from "./NowGraphKit";

// The Now card's charging graph (design_preview "now_charge_graph"), in the soft look of the parked graph: the open charge's battery from plug-in
// to now as one soft line, the charger's power as a quieter second line, and, when the card has a finish time, the way on to the target as a
// soft dotted continuation with the finish time in a small pill. The series are the Live page's (stored samples, no Rivian call), read at
// its 30 s pace. Under two minutes of samples it says so instead of drawing a stub.
const MIN_SPAN_MS = 120_000;
const PAD = { t: 30, b: 26, l: 6, r: 10 };

export function NowChargeGraph({ vehicleId, nowMs, targetPct, etaHours }: { vehicleId: string; nowMs: number; targetPct: number | null; etaHours: number | null }) {
  const { open, soc, power } = useOpenChargeSeries(vehicleId);
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
  const target = targetPct != null && targetPct > last.y ? targetPct : null;
  const endX = target != null && etaHours != null && etaHours > 0 ? Math.max(last.x, nowMs) + etaHours * 3_600_000 : null;
  const peak = power.reduce((m, p) => Math.max(m, p.y), 0);
  const lastPower = power[power.length - 1];
  const gained = Math.round(last.y - first.y);

  const x1 = endX ?? last.x;
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
  const ex = endX != null ? sx(endX) : null;
  const ey = target != null ? sy(target) : null;
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
            aria-label={`Battery from ${Math.round(first.y)}% to ${Math.round(last.y)}% since ${clock(first.x)}${lastPower ? `, charging at ${Math.round(lastPower.y)} kilowatts` : ""}${pill ? `, reaching ${pill.replace("·", "at")}` : ""}`}
            className="block overflow-visible"
          >
            <SoftDefs id={id} ink={ink} />
            {ex != null && <rect x={lx + 4} y={PAD.t - 6} width={Math.max(ex - lx - 8, 4)} height={base - PAD.t + 6} rx={12} fill={`url(#${id}-band)`} />}
            <SoftFill id={id} d={line} baseY={base} x0={socPts[0].x} x1={lx} />
            {powPts.length > 1 && <QuietLine d={softPath(powPts)} color={ink.quiet} />}
            <SoftLine id={id} d={line} color={ink.line} />
            {ex != null && ey != null && (
              <>
                <path d={`M${lx.toFixed(1)} ${ly.toFixed(1)} L${ex.toFixed(1)} ${ey.toFixed(1)}`} data-testid="now-charge-graph-estimate-line" fill="none" stroke={ink.line} strokeWidth={2.2} strokeLinecap="round" strokeDasharray="0.1 7" strokeOpacity=".8" />
                <HaloDot id={id} x={ex} y={ey} color={ink.line} />
                <Pill x={w - PAD.r} y={Math.max(ey - 18, 12)} text={pill ?? ""} w={labelWidth(pill ?? "")} anchor="end" />
              </>
            )}
            <HaloDot id={id} x={socPts[0].x} y={socPts[0].y} color={ink.line} />
            <HaloDot id={id} x={lx} y={ly} color={ink.line} big />
            {labelStartValue && (
              <text x={socPts[0].x + 2} y={Math.max(socPts[0].y - 14, 12)} className="fill-neutral-300 text-xs">
                {Math.round(first.y)}%
              </text>
            )}
            <text x={valueLeft ? lx - 10 : lx + 10} y={Math.max(ly - 12, 12)} textAnchor={valueLeft ? "end" : "start"} className="fill-neutral-100 text-sm font-semibold">
              {Math.round(last.y)}%
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
          ...(lastPower ? [{ swatch: "line" as const, color: ink.quiet, label: `Power ${Math.round(lastPower.y)} kW now` }] : []),
          ...(endX != null ? [{ swatch: "dash" as const, color: ink.line, label: "Estimate", testid: "now-charge-graph-estimate" }] : []),
        ]}
      />
    </div>
  );
}
