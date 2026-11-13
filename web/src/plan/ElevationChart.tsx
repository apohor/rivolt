import { useId, useMemo, useRef, type KeyboardEvent, type PointerEvent } from "react";
import type { ElevationProfile } from "../lib/api";
import type { DistanceUnit } from "../lib/preferences";
import { TERRAIN_ATTRIBUTION } from "../map/style";
import { elevationIn, elevationUnit, heightAt, stopShares, totalMeters } from "./elevation";

// The elevation along the route, drawn under the summary and linked to the map: pointing at, touching or dragging along the chart moves a dot on
// the route, and a tap on the route puts the chart's marker there. The chart is a slider for the keyboard and screen readers too (arrows, Home,
// End). It shows the terrain as context only: no energy figure comes from it.
const W = 330;
const H = 110;
const LEFT = 10;
const RIGHT = 320;
const TOP = 10;
const BASE = 86;
const KEY_STEP = 0.02;

const num = (n: number) => Math.round(n).toLocaleString("en-US");

export function ElevationChart({
  profile,
  share,
  onShare,
  distanceUnit,
  stopNames,
}: {
  profile: ElevationProfile;
  // Where the cursor is, 0 to 1 along the route, or null for none.
  share: number | null;
  onShare: (s: number | null) => void;
  distanceUnit: DistanceUnit;
  // The name of each waypoint by index in the route's Waypoints, for the stop labels.
  stopNames?: (string | undefined)[];
}) {
  const id = useId();
  const svgRef = useRef<SVGSVGElement>(null);
  const total = totalMeters(profile);
  const eu = elevationUnit(distanceUnit);
  const du = distanceUnit;
  const geo = useMemo(() => {
    const ys = profile.points.map(([, e]) => elevationIn(e, distanceUnit));
    const lo = Math.min(...ys);
    const hi = Math.max(...ys);
    const pad = Math.max((hi - lo) * 0.08, 1);
    const min = lo - pad;
    const max = hi + pad;
    const x = (m: number) => LEFT + (m / total) * (RIGHT - LEFT);
    const y = (v: number) => BASE - ((v - min) / (max - min)) * (BASE - TOP);
    const line = profile.points.map(([d, e], i) => `${i ? "L" : "M"}${x(d).toFixed(1)} ${y(elevationIn(e, distanceUnit)).toFixed(1)}`).join(" ");
    return { x, y, line, lo, hi };
  }, [profile, distanceUnit, total]);
  const stops = useMemo(() => stopShares(profile), [profile]);
  const dist = (m: number) => `${num(du === "mi" ? m / 1609.344 : m / 1000)} ${du}`;

  const shareAt = (e: PointerEvent) => {
    const r = svgRef.current?.getBoundingClientRect();
    if (!r || r.width <= 0) return null;
    // The plot spans LEFT..RIGHT of the W-wide viewBox.
    const px = ((e.clientX - r.left) / r.width) * W;
    return Math.min(1, Math.max(0, (px - LEFT) / (RIGHT - LEFT)));
  };
  const move = (e: PointerEvent) => {
    // A mouse hovers; a finger or pen only follows while down.
    if (e.pointerType !== "mouse" && e.buttons === 0) return;
    const s = shareAt(e);
    if (s != null) onShare(s);
  };
  const key = (e: KeyboardEvent) => {
    const cur = share ?? 0;
    let next: number | null = null;
    if (e.key === "ArrowRight" || e.key === "ArrowUp") next = Math.min(1, cur + KEY_STEP);
    else if (e.key === "ArrowLeft" || e.key === "ArrowDown") next = Math.max(0, cur - KEY_STEP);
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = 1;
    else if (e.key === "Escape") onShare(null);
    else return;
    e.preventDefault();
    if (next != null) onShare(next);
  };

  const cx = share != null ? geo.x(share * total) : null;
  const cyVal = share != null ? heightAt(profile, share) : null;
  const readout =
    share != null && cyVal != null
      ? `${dist(share * total)} · ${num(elevationIn(cyVal, distanceUnit))} ${eu}`
      : `${num(geo.lo)} to ${num(geo.hi)} ${eu}`;
  const label = `Elevation along the route, ${num(geo.lo)} to ${num(geo.hi)} ${eu}, over ${dist(total)}`;

  return (
    <section data-plan-area="elevation" aria-labelledby={`${id}-h`} className="mt-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-2">
        <h2 id={`${id}-h`} className="ui-label [hyphens:none]">Elevation</h2>
        <span data-elevation-readout className="font-mono text-xs text-neutral-400 [overflow-wrap:anywhere]">{readout}</span>
      </div>
      <svg
        ref={svgRef}
        data-elevation-chart
        viewBox={`0 0 ${W} ${H}`}
        width="100%"
        role="slider"
        tabIndex={0}
        aria-label={label}
        aria-orientation="horizontal"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={share != null ? Math.round(share * 100) : undefined}
        aria-valuetext={share != null && cyVal != null ? readout : "Not set. Use the arrow keys to move along the route."}
        className="mt-1 block h-auto w-full touch-pan-y select-none rounded-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400"
        onPointerDown={(e) => {
          if (e.pointerType === "mouse" && e.button !== 0) return;
          try { e.currentTarget.setPointerCapture(e.pointerId); } catch { /* a synthetic pointer */ }
          const s = shareAt(e);
          if (s != null) onShare(s);
        }}
        onPointerMove={move}
        onPointerLeave={(e) => { if (e.pointerType === "mouse" && e.buttons === 0) onShare(null); }}
        onKeyDown={key}
      >
        <path d={`${geo.line} L${RIGHT} ${BASE} L${LEFT} ${BASE} Z`} fill="#34d39922" />
        <path d={geo.line} fill="none" stroke="#34d399" strokeWidth={2} />
        <line x1={LEFT} y1={BASE} x2={RIGHT} y2={BASE} stroke="#2a2a2e" />
        {stops.map(({ index, share: s }) => {
          const px = geo.x(s * total);
          return (
            <g key={index} data-elevation-stop={index}>
              <line x1={px} y1={TOP} x2={px} y2={BASE} stroke="#f59e0b" strokeDasharray="3 3" />
              <circle cx={px} cy={geo.y(elevationIn(heightAt(profile, s), distanceUnit))} r={4} fill="#f59e0b" stroke="#0a0a0b" strokeWidth={2}>
                {stopNames?.[index] ? <title>{stopNames[index]}</title> : null}
              </circle>
            </g>
          );
        })}
        {cx != null && cyVal != null && (
          <g data-elevation-cursor pointerEvents="none">
            <line x1={cx} y1={TOP} x2={cx} y2={BASE} stroke="#fafafa" strokeWidth={1} />
            <circle cx={cx} cy={geo.y(elevationIn(cyVal, distanceUnit))} r={5} fill="#fafafa" stroke="#0a0a0b" strokeWidth={2} />
          </g>
        )}
        <g fill="#a3a3a3" fontSize={10} fontFamily="ui-monospace,monospace" pointerEvents="none">
          <text x={LEFT} y={102}>0</text>
          <text x={RIGHT} y={102} textAnchor="end">{dist(total)}</text>
        </g>
      </svg>
      <p className="mt-1 text-xs text-neutral-500">Elevation shown for context; Rivian&apos;s estimate decides the energy.</p>
      <p className="text-[11px] text-neutral-600">{TERRAIN_ATTRIBUTION}</p>
    </section>
  );
}
