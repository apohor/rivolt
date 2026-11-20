import { useCallback, useRef, useState } from "react";
import { LineChart } from "./charts";
import { displayTimeZone } from "../lib/format";
import { powerMax, useOpenChargeSeries } from "./LiveChargeGraph";

// The Now card's charging graph (design_preview "now_charge_graph"): the open charge's battery and power from plug-in to now, the target as a
// dashed line, and, when the card has a finish time, the battery's way to the target as a dashed estimate. The series are the Live page's
// (stored samples, no Rivian call), read at its 30 s pace. Under two minutes of samples it says so instead of drawing a stub.
const MIN_SPAN_MS = 120_000;

export function NowChargeGraph({ vehicleId, nowMs, targetPct, etaHours }: { vehicleId: string; nowMs: number; targetPct: number | null; etaHours: number | null }) {
  const { open, soc, power } = useOpenChargeSeries(vehicleId);
  // Three time labels need about 300 px; narrower (a phone at large text), fewer keep them apart.
  const [ticks, setTicks] = useState(3);
  const roRef = useRef<ResizeObserver | null>(null);
  const box = useCallback((el: HTMLDivElement | null) => {
    roRef.current?.disconnect();
    roRef.current = null;
    if (!el || typeof ResizeObserver === "undefined") return;
    roRef.current = new ResizeObserver(([e]) => setTicks(e.contentRect.width >= 300 ? 3 : e.contentRect.width >= 235 ? 2 : 1));
    roRef.current.observe(el);
  }, []);
  if (!open) return null;
  const first = soc[0]?.x ?? NaN;
  const last = soc[soc.length - 1];
  if (!last || soc.length < 3 || last.x - first < MIN_SPAN_MS) {
    return (
      <p data-testid="now-charge-graph-collecting" className="mt-3 text-xs text-neutral-400">
        Collecting this charge's first minutes. The graph appears shortly.
      </p>
    );
  }
  const peak = power.reduce((m, p) => Math.max(m, p.y), 0);
  const target = targetPct != null && targetPct > last.y ? targetPct : null;
  const endX = target != null && etaHours != null && etaHours > 0 ? Math.max(last.x, nowMs) + etaHours * 3_600_000 : null;
  const x1 = endX ?? last.x;
  const lo = Math.min(...soc.map((p) => p.y));
  const hi = Math.max(last.y, target ?? 0, ...soc.map((p) => p.y));
  return (
    <div ref={box} data-testid="now-charge-graph" className="mt-3 min-w-0">
      <div className="mb-1 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-neutral-400">
        <span className="inline-flex items-center gap-1.5"><i className="inline-block h-0.5 w-3 rounded" style={{ background: "#10b981" }} />Battery</span>
        {power.length > 0 && <span className="inline-flex items-center gap-1.5"><i className="inline-block h-0.5 w-3 rounded" style={{ background: "#f59e0b" }} />Power</span>}
        {target != null && <span className="inline-flex items-center gap-1.5"><i className="inline-block w-3 border-t border-dotted border-neutral-400" />Target {Math.round(target)}%</span>}
        {endX != null && <span data-testid="now-charge-graph-estimate" className="inline-flex items-center gap-1.5"><i className="inline-block w-3 border-t border-dashed" style={{ borderColor: "#10b981" }} />Estimate</span>}
      </div>
      <LineChart
        ariaLabel="Charging power and battery level over this charge"
        height={150}
        series={[
          { points: soc, color: "#10b981", strokeWidth: 1.4, area: true, label: "Battery" },
          ...(endX != null && target != null ? [{ points: [last, { x: endX, y: target }], color: "#10b981", strokeWidth: 1.2, dash: "4 4", label: "Estimate" }] : []),
          ...(target != null ? [{ points: [{ x: first, y: target }, { x: x1, y: target }], color: "#a3a3a3", strokeWidth: 1, dash: "2 4", label: "Target" }] : []),
          ...(power.length > 0 ? [{ points: power, color: "#f59e0b", strokeWidth: 1.2, label: "Power", axis: "right" as const }] : []),
        ]}
        xDomain={[first, x1]}
        yDomain={[Math.max(0, Math.floor(lo) - 3), Math.min(100, Math.ceil(hi) + 3)]}
        y2Domain={[0, powerMax(peak)]}
        formatY={(v) => `${v.toFixed(0)}%`}
        formatY2={(v) => `${v.toFixed(0)} kW`}
        xTicks={ticks}
        formatX={(x) => new Date(x).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit", timeZone: displayTimeZone() })}
      />
    </div>
  );
}
