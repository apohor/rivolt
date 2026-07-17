// DriveTimeline — Tier 2 telemetry view.
//
// Two stacked panels that share an x-axis and a synced cursor:
//
//   1. Speed (with a divergent power ribbon along the top — red = draw,
//      green = regen — and the drive-mode tint as a faint background
//      wash). Auto-detected moments (hard brake, max regen, top speed,
//      etc.) drop in as vertical markers.
//   2. Battery (SoC line) with an elevation area as a faint backdrop so
//      climbs visibly explain SoC drops.
//
// Below the panels: thin strip rows for precipitation and drive mode,
// then a shared time axis. A single floating tooltip anchored at the
// cursor X carries every signal — the tooltip is the legend.
//
// Power is derived from smoothed SoC and the drive's own pack capacity
// (EnergyUsedKWh / SoC delta), so it works on every drive that has
// telemetry without needing a per-vehicle pack-size config.

import {
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from "react";
import type { Drive, DriveWeatherSamplePoint, Sample } from "../lib/api";
import { buildElevPts, derivePower } from "../lib/power";
import { smoothGaussianTime } from "../lib/smooth";

type Mode = "Conserve" | "AllPurpose" | "Sport" | "Other";

const SERIES = {
  speed: "#38bdf8",
  battery: "#10b981",
  elevation: "#a78bfa",
  draw: "#ef4444",
  regen: "#22c55e",
  rain: "#3b82f6",
  drizzle: "#93c5fd",
  snow: "#e0f2fe",
  freezingRain: "#a855f7",
  thunderstorm: "#ef4444",
} as const;

const MODE_TINT: Record<Mode, string> = {
  Conserve: "#22c55e1f",
  AllPurpose: "#f973161f",
  Sport: "#ef44441f",
  Other: "transparent",
};

const MODE_BAND: Record<Mode, string> = {
  Conserve: "#22c55e",
  AllPurpose: "#f97316",
  Sport: "#ef4444",
  Other: "#a3a3a3",
};

const MODE_LABEL: Record<Mode, string> = {
  Conserve: "Conserve",
  AllPurpose: "All-Purpose",
  Sport: "Sport",
  Other: "Other",
};

export type DriveTimelineProps = {
  drive: Drive;
  samples: Sample[];
  weatherPts: DriveWeatherSamplePoint[];
  cursorMs: number | null;
  onCursorChange: (ms: number | null) => void;
  // Controlled zoom window in epoch ms. `null` means "show the full
  // drive". Lifted to the page so the route map can crop to the same
  // window. Set by drag-to-select on the chart, cleared by the reset
  // button or double-click.
  viewWindow: [number, number] | null;
  onViewWindowChange: (window: [number, number] | null) => void;
  tempUnit: "f" | "c";
};

export function DriveTimeline({
  drive,
  samples,
  weatherPts,
  cursorMs,
  onCursorChange,
  viewWindow,
  onViewWindowChange,
  tempUnit,
}: DriveTimelineProps) {
  // Track whether the user last interacted via touch so we can keep
  // the cursor alive after the finger lifts (touch has no "hover").
  const [isTouch, setIsTouch] = useState(false);
  // Tap-to-add legend: Speed / Battery / Power / Elevation are the
  // established layers and stay on by default; Pack temp and Headwind
  // are opt-in overlays that otherwise only surface in the tooltip, so
  // the chart stays useful without being overloaded. Every layer is
  // toggleable — mirrors the charge page's chip legend.
  const [showSpeed, setShowSpeed] = useState(true);
  const [showBattery, setShowBattery] = useState(true);
  const [showPower, setShowPower] = useState(true);
  const [showElevation, setShowElevation] = useState(true);
  const [showPackTemp, setShowPackTemp] = useState(false);
  const [showHeadwind, setShowHeadwind] = useState(false);
  const driveStartMs = new Date(drive.StartedAt).getTime();
  const driveEndMs = new Date(drive.EndedAt).getTime();

  const speedPts = useMemo(() => buildSpeedPts(samples), [samples]);
  const socPts = useMemo(() => {
    const raw = samples.map((p) => ({
      x: new Date(p.At).getTime(),
      y: p.BatteryLevelPct || 0,
    }));
    return smoothGaussianTime(raw, 30_000);
  }, [samples]);
  const elevPts = useMemo(() => buildElevPts(samples), [samples]);
  const powerPts = useMemo(
    () => derivePower(samples, elevPts, drive.EnergyUsedKWh),
    [samples, elevPts, drive.EnergyUsedKWh],
  );

  const modeSegments = useMemo(
    () => buildModeSegments(samples, driveStartMs, driveEndMs),
    [samples, driveStartMs, driveEndMs],
  );
  const parkBands = useMemo(
    () => buildParkBands(samples, driveStartMs, driveEndMs),
    [samples, driveStartMs, driveEndMs],
  );
  const precipBands = useMemo(
    () => buildPrecipBands(weatherPts, driveStartMs, driveEndMs),
    [weatherPts, driveStartMs, driveEndMs],
  );
  const headwindPts = useMemo(
    () => buildHeadwindPts(weatherPts, driveStartMs, driveEndMs),
    [weatherPts, driveStartMs, driveEndMs],
  );
  // Battery pack avg cell temperature — an opt-in overlay on the
  // battery panel. Smoothed lightly since the sensor is coarse.
  const packTempPts = useMemo(() => {
    const raw = samples
      .filter((p) => p.pack_temp_avg_c != null && p.pack_temp_avg_c !== 0)
      .map((p) => ({ x: new Date(p.At).getTime(), y: p.pack_temp_avg_c as number }));
    return raw.length > 1 ? smoothGaussianTime(raw, 60_000) : raw;
  }, [samples]);
  const hasPackTemp = packTempPts.length > 0;
  const hasHeadwind = headwindPts.length > 0;
  const moments = useMemo(
    () => detectMoments(speedPts, socPts, powerPts, headwindPts),
    [speedPts, socPts, powerPts, headwindPts],
  );

  // cursorSample lives BEFORE the empty-samples early return so all
  // hooks fire in the same order on every render — Rules-of-Hooks.
  // Even though the parent guards driveSamples > 0 so this branch
  // never fires in practice today, the linter (correctly) sees the
  // structural violation and flags it, and a future caller passing
  // empty samples would trigger it at runtime.
  const cursorSample = useMemo(() => {
    if (cursorMs == null || samples.length === 0) return null;
    let best = samples[0];
    let bestD = Math.abs(new Date(best.At).getTime() - cursorMs);
    for (let i = 1; i < samples.length; i++) {
      const d = Math.abs(new Date(samples[i].At).getTime() - cursorMs);
      if (d < bestD) {
        bestD = d;
        best = samples[i];
      }
    }
    return best;
  }, [cursorMs, samples]);

  if (speedPts.length === 0 && socPts.length === 0) {
    return <NoSamples />;
  }

  // Full-drive x-domain: anchored to the drive's own coverage so
  // weather padding doesn't compress the actual driving into a narrow
  // middle strip.
  const allX = [
    ...speedPts.map((p) => p.x),
    ...socPts.map((p) => p.x),
    ...elevPts.map((p) => p.x),
  ];
  const fullXMin = allX.length > 0 ? Math.min(...allX) : driveStartMs;
  const fullXMax = allX.length > 0 ? Math.max(...allX) : driveEndMs;

  // Visible x-domain: clamp the lifted viewWindow to the drive's own
  // coverage and fall back to the full drive when no zoom is active.
  // Clamping defends against navigating in from a stale URL or a
  // moments-detection bug that produces an out-of-range window.
  const xMin = viewWindow
    ? Math.max(fullXMin, Math.min(viewWindow[0], viewWindow[1]))
    : fullXMin;
  const xMax = viewWindow
    ? Math.min(fullXMax, Math.max(viewWindow[0], viewWindow[1]))
    : fullXMax;
  const isZoomed = viewWindow != null;

  const cursorPower = nearestY(powerPts, cursorMs);
  const cursorSoc = nearestY(socPts, cursorMs);
  const cursorElev = nearestY(elevPts, cursorMs);
  const cursorHeadwind = nearestY(headwindPts, cursorMs);

  return (
    <div className="space-y-3">
      {/* Tap-to-add legend: toggle any layer. Speed / Battery / Power /
          Elevation default on; Pack temp + Headwind are opt-in and only
          appear as chips when the drive actually carries that data. */}
      <div className="flex flex-wrap items-center gap-1.5">
        <TimelineChip label="Speed" color={SERIES.speed} on={showSpeed} onClick={() => setShowSpeed((v) => !v)} />
        <TimelineChip label="Battery" color={SERIES.battery} on={showBattery} onClick={() => setShowBattery((v) => !v)} />
        <TimelineChip label="Power" color="#f59e0b" on={showPower} onClick={() => setShowPower((v) => !v)} />
        {elevPts.length > 1 ? (
          <TimelineChip label="Elevation" color={SERIES.elevation} on={showElevation} onClick={() => setShowElevation((v) => !v)} />
        ) : null}
        {hasPackTemp ? (
          <TimelineChip label="Pack temp" color="#f472b6" on={showPackTemp} onClick={() => setShowPackTemp((v) => !v)} />
        ) : null}
        {hasHeadwind ? (
          <TimelineChip label="Headwind" color="#0891b2" on={showHeadwind} onClick={() => setShowHeadwind((v) => !v)} />
        ) : null}
      </div>
      <TimelineSVG
        xMin={xMin}
        xMax={xMax}
        speedPts={speedPts}
        socPts={socPts}
        elevPts={elevPts}
        powerPts={powerPts}
        packTempPts={packTempPts}
        headwindPts={headwindPts}
        modeSegments={modeSegments}
        parkBands={parkBands}
        precipBands={precipBands}
        moments={moments}
        cursorMs={cursorMs}
        onCursorChange={onCursorChange}
        onViewWindowChange={onViewWindowChange}
        onPointerTypeChange={setIsTouch}
        isTouch={isTouch}
        cursorSample={cursorSample}
        cursorPower={cursorPower}
        cursorSoc={cursorSoc}
        cursorElev={cursorElev}
        cursorHeadwind={cursorHeadwind}
        tempUnit={tempUnit}
        maxSpeedMph={drive.MaxSpeedMph}
        startSoC={drive.StartSoCPct}
        endSoC={drive.EndSoCPct}
        showSpeed={showSpeed}
        showBattery={showBattery}
        showPower={showPower}
        showElevation={showElevation}
        showPackTemp={showPackTemp}
        showHeadwind={showHeadwind}
      />

      {/* HTML cursor readout — always readable regardless of SVG scale.
          On desktop it mirrors the floating tooltip; on touch it's the
          primary way to inspect a sample (no hover on mobile). Hidden
          when no cursor is active. */}
      {cursorSample ? (
        <CursorReadout
          cursorSample={cursorSample}
          cursorPower={cursorPower}
          cursorSoc={cursorSoc}
          cursorElev={cursorElev}
          cursorHeadwind={cursorHeadwind}
          tempUnit={tempUnit}
          onDismiss={isTouch ? () => onCursorChange(null) : undefined}
        />
      ) : null}

      <div className="flex items-center justify-between gap-3">
        {moments.length > 0 ? (
          <MomentChips
            moments={moments}
            cursorMs={cursorMs}
            onCursorChange={(ms) => {
              onCursorChange(ms);
              // If a chip click jumps the cursor out of the visible
              // window, reset zoom so the marker isn't off-screen.
              if (ms != null && (ms < xMin || ms > xMax)) {
                onViewWindowChange(null);
              }
            }}
          />
        ) : (
          <span />
        )}
        {isZoomed ? (
          <button
            type="button"
            onClick={() => onViewWindowChange(null)}
            className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-neutral-700 bg-neutral-900 px-2.5 py-0.5 text-[11px] font-medium text-neutral-300 hover:bg-neutral-800"
            title="Reset chart zoom — also re-fits the route map"
          >
            <span aria-hidden>⤢</span>
            Reset zoom
          </button>
        ) : (
          <span className="hidden text-[10px] text-neutral-600 sm:inline">
            Drag to zoom · double-click to reset
          </span>
        )}
      </div>
    </div>
  );
}

// ---- Layout ---------------------------------------------------------------

const VIEW_W = 1000;
// Both gutters carry a y-axis: Speed on the left (left-anchored ticks),
// SoC on the right. 70/44 px fit a 3-digit tick label plus breathing
// room at fontSize=10.
const PAD_L = 70;
const PAD_R = 44;
const PLOT_W = VIEW_W - PAD_L - PAD_R;

// Dead-zone compression: a parked stretch or telemetry gap is drawn at a
// fixed narrow width instead of consuming its real duration on the axis,
// so a 39-minute stop can't crush the actual driving into the margins.
// In viewBox units (of PLOT_W ≈ 950) — ~3% each, enough to show the
// dashed marker + label without dominating.
const COMPRESSED_BAND_PX = 34;

// One shared plot panel. Speed and SoC overlay in the same box on
// independent axes (left = mph, right = %); the two-panel stack was
// consolidated into a single graph to match the charge detail view.
const RIBBON_TOP = 8;
const RIBBON_H = 10;
const PLOT_TOP = RIBBON_TOP + RIBBON_H + 4;
const PLOT_H = 190;
// Speed maps to the left axis, SoC to the right — both over the full
// panel. Kept as named aliases so every data layer reads clearly.
const SPEED_TOP = PLOT_TOP;
const SPEED_H = PLOT_H;
const BATT_TOP = PLOT_TOP;
const BATT_H = PLOT_H;
const PRECIP_TOP = PLOT_TOP + PLOT_H + 8;
const PRECIP_H = 12;
const MODE_STRIP_TOP = PRECIP_TOP + PRECIP_H + 3;
const MODE_STRIP_H = 12;
const AXIS_TOP = MODE_STRIP_TOP + MODE_STRIP_H + 6;
const AXIS_H = 16;
const TOTAL_H = AXIS_TOP + AXIS_H + 4;

type ModeSegment = { x0: number; x1: number; mode: Mode };
type ParkBand = { x0: number; x1: number; kind: "parked" | "gap" };

// A piecewise segment of the warped time axis: data-ms [m0,m1] maps to
// pixel [p0,p1]. Dead segments (parked / gap) get a fixed narrow width.
type TimeSeg = { m0: number; m1: number; p0: number; p1: number; dead: boolean };

// buildTimeScale warps the linear time axis so parked stretches and
// telemetry gaps take a fixed narrow width (bandPx) instead of their real
// duration, and the active driving time fills the rest proportionally.
// Returns sx (ms→px) and its inverse; both are continuous and monotonic so
// the cursor, brush and every data layer stay consistent. With no dead
// zones it degrades to the original linear mapping.
function buildTimeScale(
  xMin: number,
  xMax: number,
  bands: ParkBand[],
  plotLeft: number,
  plotW: number,
  bandPx: number,
): { sx: (ms: number) => number; invert: (px: number) => number } {
  const dead = bands
    .map((b) => ({ x0: Math.max(xMin, b.x0), x1: Math.min(xMax, b.x1) }))
    .filter((b) => b.x1 - b.x0 > 1)
    .sort((a, b) => a.x0 - b.x0);
  const merged: { x0: number; x1: number }[] = [];
  for (const b of dead) {
    const last = merged[merged.length - 1];
    if (last && b.x0 <= last.x1) last.x1 = Math.max(last.x1, b.x1);
    else merged.push({ ...b });
  }
  const span = Math.max(1, xMax - xMin);
  const deadDur = merged.reduce((s, b) => s + (b.x1 - b.x0), 0);
  const activeDur = Math.max(1, span - deadDur);
  const activePx = Math.max(1, plotW - merged.length * bandPx);
  const pxPerActive = activePx / activeDur;

  const segs: TimeSeg[] = [];
  let cm = xMin;
  let cp = plotLeft;
  for (const b of merged) {
    if (b.x0 > cm) {
      const w = (b.x0 - cm) * pxPerActive;
      segs.push({ m0: cm, m1: b.x0, p0: cp, p1: cp + w, dead: false });
      cp += w;
    }
    segs.push({ m0: b.x0, m1: b.x1, p0: cp, p1: cp + bandPx, dead: true });
    cp += bandPx;
    cm = b.x1;
  }
  if (cm < xMax || segs.length === 0) {
    segs.push({ m0: cm, m1: xMax, p0: cp, p1: plotLeft + plotW, dead: false });
  }

  const sx = (ms: number): number => {
    if (ms <= xMin) return plotLeft;
    if (ms >= xMax) return plotLeft + plotW;
    for (const s of segs) {
      if (ms <= s.m1) {
        const t = (ms - s.m0) / Math.max(1, s.m1 - s.m0);
        return s.p0 + t * (s.p1 - s.p0);
      }
    }
    return plotLeft + plotW;
  };
  const invert = (px: number): number => {
    if (px <= plotLeft) return xMin;
    if (px >= plotLeft + plotW) return xMax;
    for (const s of segs) {
      if (px <= s.p1) {
        const t = (px - s.p0) / Math.max(1e-6, s.p1 - s.p0);
        return s.m0 + t * (s.m1 - s.m0);
      }
    }
    return xMax;
  };
  return { sx, invert };
}
type PrecipBand = {
  x0: number;
  x1: number;
  color: string;
  label: string;
};
type Moment = {
  ms: number;
  label: string;
  detail: string;
  tone: "info" | "warning" | "success" | "neutral";
};

function TimelineSVG(props: {
  xMin: number;
  xMax: number;
  speedPts: { x: number; y: number }[];
  socPts: { x: number; y: number }[];
  elevPts: { x: number; y: number }[];
  powerPts: { x: number; y: number }[];
  packTempPts: { x: number; y: number }[];
  headwindPts: { x: number; y: number }[];
  modeSegments: ModeSegment[];
  parkBands: ParkBand[];
  precipBands: PrecipBand[];
  moments: Moment[];
  cursorMs: number | null;
  onCursorChange: (ms: number | null) => void;
  onViewWindowChange: (window: [number, number] | null) => void;
  // Called with true when a touch pointer is first detected, false
  // when the user switches back to mouse. DriveTimeline uses this to
  // decide whether to show the HTML readout dismiss button.
  onPointerTypeChange: (isTouch: boolean) => void;
  isTouch: boolean;
  cursorSample: Sample | null;
  cursorPower: number | null;
  cursorSoc: number | null;
  cursorElev: number | null;
  cursorHeadwind: number | null;
  tempUnit: "f" | "c";
  maxSpeedMph: number;
  startSoC: number;
  endSoC: number;
  // Layer visibility from the tap-to-add legend.
  showSpeed: boolean;
  showBattery: boolean;
  showPower: boolean;
  showElevation: boolean;
  showPackTemp: boolean;
  showHeadwind: boolean;
}) {
  const {
    xMin,
    xMax,
    speedPts,
    socPts,
    elevPts,
    powerPts,
    packTempPts,
    headwindPts,
    modeSegments,
    parkBands,
    precipBands,
    moments,
    cursorMs,
    onCursorChange,
    onViewWindowChange,
    onPointerTypeChange,
    isTouch,
    cursorSample,
    cursorPower,
    cursorSoc,
    cursorElev,
    cursorHeadwind,
    tempUnit,
    maxSpeedMph,
    startSoC,
    endSoC,
    showSpeed,
    showBattery,
    showPower,
    showElevation,
    showPackTemp,
    showHeadwind,
  } = props;

  // The SVG uses preserveAspectRatio="none" so the fixed viewBox stretches
  // to fill the container — great for the data paths, but it distorts
  // *text* (squished-narrow on a phone, wide on a desktop). Measure the
  // rendered box and counter-scale every text element by the inverse
  // stretch so labels stay upright at any width. fx/fy default to 1 (no
  // distortion) until the first measurement lands.
  const svgRef = useRef<SVGSVGElement | null>(null);
  const [aspect, setAspect] = useState({ fx: 1, fy: 1 });
  useLayoutEffect(() => {
    const el = svgRef.current;
    if (!el) return;
    const measure = () => {
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.height > 0) {
        setAspect({ fx: r.width / VIEW_W, fy: r.height / TOTAL_H });
      }
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  // Counter-scale factor for text: undo the SVG stretch around each label's
  // anchor point. Applied via <g transform> so textAnchor still works.
  const textTF = (x: number, y: number) =>
    `translate(${x} ${y}) scale(${1 / aspect.fx} ${1 / aspect.fy})`;

  const xSpan = Math.max(1, xMax - xMin);
  // Warped time axis: parked/gap dead zones compressed to a fixed width so
  // real driving data owns the plot. sx + invert stay continuous, so every
  // layer (data, bands, ticks, cursor, brush) follows automatically.
  const scale = buildTimeScale(
    xMin,
    xMax,
    parkBands,
    PAD_L,
    PLOT_W,
    COMPRESSED_BAND_PX,
  );
  const sx = scale.sx;

  // Brush state — local to the chart. dragStart/dragEnd are in
  // data-space ms, captured during a pointerdown→move→up gesture.
  // We treat <5 px of pointer movement as a click (no drag), so
  // single-clicks still fall through to the existing cursor-set
  // behavior; only an actual drag commits a zoom.
  const [drag, setDrag] = useState<{
    startMs: number;
    endMs: number;
    startScreenX: number;
    moved: boolean;
  } | null>(null);

  const eventToDataMs = (
    e: ReactPointerEvent<SVGElement>,
  ): number | null => {
    const svg = e.currentTarget.ownerSVGElement;
    if (!svg) return null;
    const rect = svg.getBoundingClientRect();
    if (rect.width === 0) return null;
    const vbX = ((e.clientX - rect.left) / rect.width) * VIEW_W;
    if (vbX < PAD_L || vbX > VIEW_W - PAD_R) return null;
    return scale.invert(vbX);
  };

  // Speed Y-domain: 0 → max speed + headroom, with a 50 mph minimum so
  // city-only drives still get a reasonable scale.
  const speedMax = Math.max(50, Math.ceil((maxSpeedMph + 5) / 10) * 10);
  const speedTicks = [0, Math.round(speedMax / 2), speedMax];

  // Battery Y-domain: tight band around the actual SoC range so the
  // line uses the panel's full height. ±5% pads against regen bumps and
  // the smoothing edges.
  const socLo = Math.max(0, Math.floor(endSoC - 5));
  const socHi = Math.min(100, Math.ceil(startSoC + 5));
  const socTicks = [socLo, Math.round((socLo + socHi) / 2), socHi];

  // Power ribbon scale: dynamic cap so quiet drives still show contrast
  // and aggressive drives don't all saturate into the same red.
  const powerCap = Math.max(60, ...powerPts.map((p) => Math.abs(p.y))) * 1.05;

  // Cursor visibility: when zoomed, hide crosshair / dots / floating
  // tooltip if the cursor is parked outside the visible window. The
  // persistent readout above the chart still shows it. This avoids
  // ghost dots floating in the y-axis gutter or out past the right
  // edge after a zoom-in.
  const cursorVisible =
    cursorMs != null && cursorMs >= xMin && cursorMs <= xMax;

  return (
    <svg
      ref={svgRef}
      viewBox={`0 0 ${VIEW_W} ${TOTAL_H}`}
      // Comfortable floor on phones so the single plot panel + strips
      // stay legible; desktop uses the viewBox aspect.
      className="w-full min-h-[320px] sm:min-h-[260px]"
      preserveAspectRatio="none"
      role="img"
      aria-label="Drive timeline"
    >
      <defs>
        {/* Single clip-path that all data layers inherit. Bounds the
            plot area horizontally so zoomed-in series can't bleed
            into the y-axis gutter or the right margin; vertically it
            covers the full SVG so individual panels don't need their
            own clip. Y-axis tick labels and panel labels live OUTSIDE
            this clip's X range and so render unaffected. */}
        <clipPath id="dt-plot-clip">
          <rect x={PAD_L} y={0} width={PLOT_W} height={TOTAL_H} />
        </clipPath>
      </defs>

      {/* ---- Mode tint background on speed panel ------------------ */}
      <g clipPath="url(#dt-plot-clip)">
        {modeSegments.map((seg, i) => (
          <rect
            key={`tint-${i}`}
            x={sx(seg.x0)}
            y={SPEED_TOP}
            width={Math.max(0, sx(seg.x1) - sx(seg.x0))}
            height={SPEED_H}
            fill={MODE_TINT[seg.mode]}
          />
        ))}
      </g>

      {/* ---- Park / gap bands across speed + battery panels -------- */}
      <g clipPath="url(#dt-plot-clip)">
        {parkBands.map((b, i) => {
          const x = sx(b.x0);
          const w = Math.max(2, sx(b.x1) - sx(b.x0));
          const mid = x + w / 2;
          const mins = Math.round((b.x1 - b.x0) / 60000);
          const fmtDur = (m: number) =>
            m >= 60 ? `${Math.floor(m / 60)}h${m % 60 ? ` ${m % 60}m` : ""}` : `${m}m`;
          const isGap = b.kind === "gap";
          const label = isGap ? `No telemetry ${fmtDur(mins)}` : `Parked ${fmtDur(mins)}`;
          return (
            <g key={`park-${i}`}>
              <rect
                x={x}
                y={SPEED_TOP}
                width={w}
                height={BATT_TOP + BATT_H - SPEED_TOP}
                fill="#171717"
                fillOpacity={isGap ? 0.35 : 0.55}
              />
              {/* Always label the (now-compressed) band; counter-scaled
                  and allowed to overflow the narrow rect, since the dead
                  zone it labels has no data to collide with. */}
              <g transform={textTF(mid, SPEED_TOP + 12)}>
                <text
                  textAnchor="middle"
                  className={isGap ? "fill-neutral-400" : "fill-neutral-300"}
                  fontSize="10"
                  fontWeight="600"
                >
                  {label}
                </text>
              </g>
            </g>
          );
        })}
      </g>

      {/* ---- Power ribbon ----------------------------------------- */}
      {/* PowerRibbon manages its own label; data inside is clipped. */}
      {showPower ? (
        <PowerRibbon powerPts={powerPts} sx={sx} cap={powerCap} textTF={textTF} />
      ) : null}

      {/* ---- Speed panel: frame + grid + ticks -------------------- */}
      <rect
        x={PAD_L}
        y={SPEED_TOP}
        width={PLOT_W}
        height={SPEED_H}
        fill="transparent"
        className="stroke-neutral-800"
        strokeWidth={0.7}
      />
      {speedTicks.map((tv, i) => {
        const y = ySolve(tv, 0, speedMax, SPEED_TOP, SPEED_H);
        return (
          <g key={`st-${i}`}>
            <line
              x1={PAD_L}
              x2={VIEW_W - PAD_R}
              y1={y}
              y2={y}
              className="stroke-neutral-800"
              strokeWidth={0.5}
              strokeDasharray={
                i === 0 || i === speedTicks.length - 1 ? undefined : "2 3"
              }
            />
            <g transform={textTF(PAD_L - 6, y + 3.5)}>
              <text
                textAnchor="end"
                className="fill-neutral-500"
                fontSize={10}
              >
                {tv}
              </text>
            </g>
          </g>
        );
      })}
      <g transform={textTF(4, SPEED_TOP + 12)}>
        <text
          fontSize={10}
          fill={SERIES.speed}
          style={{ textTransform: "uppercase", letterSpacing: 0.6 }}
        >
          Speed
        </text>
      </g>

      {/* ---- Speed area + line ------------------------------------ */}
      {showSpeed && speedPts.length > 1
        ? (() => {
            const path = monotonePath(
              speedPts.map((p) => ({
                x: sx(p.x),
                y: ySolve(p.y, 0, speedMax, SPEED_TOP, SPEED_H),
              })),
            );
            const last = speedPts[speedPts.length - 1];
            const first = speedPts[0];
            const baseY = ySolve(0, 0, speedMax, SPEED_TOP, SPEED_H);
            return (
              <g clipPath="url(#dt-plot-clip)">
                <path
                  d={`${path} L ${sx(last.x).toFixed(2)},${baseY.toFixed(2)} L ${sx(first.x).toFixed(2)},${baseY.toFixed(2)} Z`}
                  fill={SERIES.speed}
                  opacity={0.12}
                />
                <path
                  d={path}
                  fill="none"
                  stroke={SERIES.speed}
                  strokeWidth={1.4}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  vectorEffect="non-scaling-stroke"
                />
              </g>
            );
          })()
        : null}

      {/* ---- Headwind overlay (opt-in) ---------------------------- */}
      {/* Dashed line scaled to its own +head / −tail range across the
          speed panel; exact values come from the tooltip/readout. */}
      {showHeadwind && headwindPts.length > 1
        ? (() => {
            const ys = headwindPts.map((p) => p.y);
            const lo = Math.min(0, ...ys);
            const hi = Math.max(1, ...ys);
            const path = monotonePath(
              headwindPts.map((p) => ({
                x: sx(p.x),
                // Confine to the lower 60% of the panel so it stays clear
                // of the speed trace's usual working range up top.
                y: ySolve(p.y, lo, hi, SPEED_TOP + SPEED_H * 0.4, SPEED_H * 0.6),
              })),
            );
            return (
              <g clipPath="url(#dt-plot-clip)">
                <path
                  d={path}
                  fill="none"
                  stroke="#0891b2"
                  strokeWidth={1}
                  strokeDasharray="4 3"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  vectorEffect="non-scaling-stroke"
                  opacity={0.85}
                />
              </g>
            );
          })()
        : null}

      {/* ---- Moment markers --------------------------------------- */}
      <g clipPath="url(#dt-plot-clip)">
        {moments.map((m, i) => {
          const x = sx(m.ms);
          return (
            <g key={`mom-${i}`}>
              <line
                x1={x}
                x2={x}
                y1={SPEED_TOP + 4}
                y2={SPEED_TOP + SPEED_H - 2}
                stroke={momentColor(m.tone)}
                strokeWidth={0.7}
                strokeDasharray="2 4"
                opacity={0.8}
              />
              <circle
                cx={x}
                cy={SPEED_TOP + 4}
                r={2.6}
                fill={momentColor(m.tone)}
              >
                <title>{`${m.label} — ${m.detail}`}</title>
              </circle>
            </g>
          );
        })}
      </g>

      {/* ---- SoC right axis (shares the single plot panel) -------- */}
      {/* No full-width gridlines here — the left (speed) axis already
          draws them; the right axis contributes only edge ticks +
          labels so the two scales stay visually separable. */}
      {socTicks.map((tv, i) => {
        const y = ySolve(tv, socLo, socHi, BATT_TOP, BATT_H);
        return (
          <g key={`bt-${i}`}>
            <line
              x1={VIEW_W - PAD_R}
              x2={VIEW_W - PAD_R + 3}
              y1={y}
              y2={y}
              stroke={SERIES.battery}
              strokeWidth={0.6}
              opacity={0.7}
            />
            <g transform={textTF(VIEW_W - PAD_R + 6, y + 3.5)}>
              <text
                textAnchor="start"
                fontSize={10}
                fill={SERIES.battery}
                fillOpacity={0.85}
              >
                {tv}
              </text>
            </g>
          </g>
        );
      })}
      <g transform={textTF(VIEW_W - PAD_R - 2, BATT_TOP + 12)}>
        <text
          textAnchor="end"
          fontSize={10}
          fill={SERIES.battery}
          style={{ textTransform: "uppercase", letterSpacing: 0.6 }}
        >
          SoC
        </text>
      </g>

      {showElevation ? (
        <ElevationBackdrop
          elevPts={elevPts}
          sx={sx}
          top={BATT_TOP}
          height={BATT_H}
        />
      ) : null}

      {/* ---- Pack temp overlay (opt-in) --------------------------- */}
      {/* Dashed pink line scaled to its own range within the battery
          panel; exact °C/°F read out in the tooltip/readout. */}
      {showPackTemp && packTempPts.length > 1
        ? (() => {
            const ys = packTempPts.map((p) => p.y);
            const tLo = Math.min(...ys);
            const tHi = Math.max(tLo + 1, Math.max(...ys));
            // Confine to the middle band of the panel so it doesn't ride
            // the SoC line or hug the frame edges.
            const path = monotonePath(
              packTempPts.map((p) => ({
                x: sx(p.x),
                y: ySolve(p.y, tLo, tHi, BATT_TOP + BATT_H * 0.15, BATT_H * 0.7),
              })),
            );
            return (
              <g clipPath="url(#dt-plot-clip)">
                <path
                  d={path}
                  fill="none"
                  stroke="#f472b6"
                  strokeWidth={1}
                  strokeDasharray="4 3"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  vectorEffect="non-scaling-stroke"
                  opacity={0.85}
                />
              </g>
            );
          })()
        : null}

      {/* ---- SoC line --------------------------------------------- */}
      {showBattery && socPts.length > 1
        ? (() => {
            const path = monotonePath(
              socPts.map((p) => ({
                x: sx(p.x),
                y: ySolve(p.y, socLo, socHi, BATT_TOP, BATT_H),
              })),
            );
            return (
              <g clipPath="url(#dt-plot-clip)">
                <path
                  d={path}
                  fill="none"
                  stroke={SERIES.battery}
                  strokeWidth={1.4}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  vectorEffect="non-scaling-stroke"
                />
              </g>
            );
          })()
        : null}

      {/* ---- Precipitation strip ---------------------------------- */}
      <CategoricalStrip
        title="Precip"
        top={PRECIP_TOP}
        height={PRECIP_H}
        bands={precipBands.map((b) => ({
          x0: sx(b.x0),
          x1: sx(b.x1),
          color: b.color,
          label: b.label,
        }))}
        showLabels={false}
        textTF={textTF}
      />

      {/* ---- Mode strip ------------------------------------------- */}
      <CategoricalStrip
        title="Mode"
        top={MODE_STRIP_TOP}
        height={MODE_STRIP_H}
        bands={modeSegments.map((seg) => ({
          x0: sx(seg.x0),
          x1: sx(seg.x1),
          color: MODE_BAND[seg.mode],
          label: MODE_LABEL[seg.mode],
        }))}
        showLabels
        textTF={textTF}
      />

      {/* ---- Time axis -------------------------------------------- */}
      <TimeAxis xMin={xMin} xMax={xMax} sx={sx} top={AXIS_TOP} textTF={textTF} />

      {/* ---- Crosshair -------------------------------------------- */}
      {cursorVisible
        ? (() => {
            const cx = sx(cursorMs!);
            return (
              <g clipPath="url(#dt-plot-clip)">
                <line
                  x1={cx}
                  x2={cx}
                  y1={SPEED_TOP}
                  y2={MODE_STRIP_TOP + MODE_STRIP_H}
                  stroke="#a3a3a3"
                  strokeWidth={1}
                  vectorEffect="non-scaling-stroke"
                />
              </g>
            );
          })()
        : null}

      {/* ---- Cursor dots on each panel ---------------------------- */}
      {cursorVisible && cursorSample ? (
        <g pointerEvents="none" clipPath="url(#dt-plot-clip)">
          {showSpeed ? (
            <circle
              cx={sx(cursorMs!)}
              cy={ySolve(
                cursorSample.SpeedMph || 0,
                0,
                speedMax,
                SPEED_TOP,
                SPEED_H,
              )}
              r={3}
              fill={SERIES.speed}
              stroke="#0a0a0a"
              strokeWidth={1.5}
            />
          ) : null}
          {showBattery && cursorSoc != null ? (
            <circle
              cx={sx(cursorMs!)}
              cy={ySolve(cursorSoc, socLo, socHi, BATT_TOP, BATT_H)}
              r={3}
              fill={SERIES.battery}
              stroke="#0a0a0a"
              strokeWidth={1.5}
            />
          ) : null}
        </g>
      ) : null}

      {/* ---- Floating tooltip (mouse only) ------------------------- */}
      {/* Shown for mouse/trackpad only. On touch the SVG scales down
          to unreadable sizes and the HTML CursorReadout below the
          chart is the primary data display instead. */}
      {!isTouch && cursorVisible && cursorSample ? (
        <FloatingTooltip
          x={sx(cursorMs!)}
          cursorSample={cursorSample}
          cursorPower={cursorPower}
          cursorSoc={cursorSoc}
          cursorElev={cursorElev}
          cursorHeadwind={cursorHeadwind}
          tempUnit={tempUnit}
        />
      ) : null}

      {/* ---- Brush-selection overlay (drawn during a drag) -------- */}
      {drag && drag.moved ? (
        <g pointerEvents="none" clipPath="url(#dt-plot-clip)">
          {(() => {
            const a = sx(Math.min(drag.startMs, drag.endMs));
            const b = sx(Math.max(drag.startMs, drag.endMs));
            return (
              <>
                <rect
                  x={a}
                  y={SPEED_TOP}
                  width={Math.max(0, b - a)}
                  height={MODE_STRIP_TOP + MODE_STRIP_H - SPEED_TOP}
                  fill="#10b981"
                  opacity={0.12}
                />
                <line
                  x1={a}
                  x2={a}
                  y1={SPEED_TOP}
                  y2={MODE_STRIP_TOP + MODE_STRIP_H}
                  stroke="#10b981"
                  strokeWidth={1}
                />
                <line
                  x1={b}
                  x2={b}
                  y1={SPEED_TOP}
                  y2={MODE_STRIP_TOP + MODE_STRIP_H}
                  stroke="#10b981"
                  strokeWidth={1}
                />
              </>
            );
          })()}
        </g>
      ) : null}

      {/* ---- Pointer-capture overlay ------------------------------ */}
      <rect
        x={PAD_L}
        y={SPEED_TOP}
        width={PLOT_W}
        height={MODE_STRIP_TOP + MODE_STRIP_H - SPEED_TOP}
        fill="transparent"
        // pan-y: vertical drag scrolls the page; horizontal drag selects a
        // time window and a tap sets the cursor. Without it the timeline
        // traps every touch gesture and the page can't scroll past it.
        style={{
          cursor: drag?.moved ? "ew-resize" : "crosshair",
          touchAction: "pan-y",
        }}
        onPointerDown={(e) => {
          // Only respond to the primary button. Ignore right-clicks
          // and middle-clicks so context menus / autoscroll still work.
          if (e.button !== 0) return;
          const touch = e.pointerType === "touch" || e.pointerType === "pen";
          onPointerTypeChange(touch);
          const t = eventToDataMs(e);
          if (t == null) return;
          (e.currentTarget as Element).setPointerCapture?.(e.pointerId);
          setDrag({
            startMs: t,
            endMs: t,
            startScreenX: e.clientX,
            moved: false,
          });
        }}
        onPointerMove={(e) => {
          if (drag) {
            const t = eventToDataMs(e);
            if (t == null) return;
            // Promote a click to a drag once the pointer has actually
            // moved a few px on screen — below that threshold we treat
            // pointerup as a tap and fall through to setting the cursor.
            // Use a larger threshold for touch (8 px) to absorb finger
            // jitter on press.
            const touchDrag =
              e.pointerType === "touch" || e.pointerType === "pen";
            const threshold = touchDrag ? 8 : 4;
            const moved =
              drag.moved || Math.abs(e.clientX - drag.startScreenX) > threshold;
            setDrag({ ...drag, endMs: t, moved });
          } else {
            // Mouse-only: update cursor continuously while hovering.
            if (e.pointerType !== "mouse") return;
            const t = eventToDataMs(e);
            if (t == null) return;
            onCursorChange(snapToSample(t, speedPts));
          }
        }}
        onPointerUp={(e) => {
          if (!drag) return;
          (e.currentTarget as Element).releasePointerCapture?.(e.pointerId);
          if (drag.moved) {
            const lo = Math.min(drag.startMs, drag.endMs);
            const hi = Math.max(drag.startMs, drag.endMs);
            // Reject windows that are smaller than 1 % of the visible
            // span — likely an accidental drag, and zooming that far in
            // would just leave the chart empty.
            if (hi - lo > xSpan * 0.01) {
              onViewWindowChange([lo, hi]);
            }
          } else {
            // No movement → treat as a tap that sets the cursor at the
            // press location (works for both mouse click and touch tap).
            onCursorChange(snapToSample(drag.startMs, speedPts));
          }
          setDrag(null);
        }}
        onPointerCancel={() => setDrag(null)}
        onPointerLeave={(e) => {
          // For mouse: clear the cursor so the readout disappears when
          // the pointer exits the chart. For touch/pen: keep the cursor
          // alive so the HTML readout stays visible after the finger
          // lifts — the user can dismiss it explicitly.
          if (!drag && e.pointerType === "mouse") onCursorChange(null);
        }}
        onDoubleClick={() => {
          // Reset zoom on a double-click anywhere over the chart.
          onViewWindowChange(null);
        }}
      />
    </svg>
  );
}

// ---- Subcomponents --------------------------------------------------------

// TimelineChip is one entry in the tap-to-add legend above the chart —
// filled when its layer is on, outlined when off. Mirrors the charge
// page's SeriesChip so the two detail views feel consistent.
function TimelineChip({
  label,
  color,
  on,
  onClick,
}: {
  label: string;
  color: string;
  on: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11px] font-medium transition-colors ${
        on
          ? "border-neutral-600 bg-neutral-800 text-neutral-100"
          : "border-neutral-800 bg-neutral-950 text-neutral-500 hover:text-neutral-300"
      }`}
      aria-pressed={on}
    >
      <span
        className="inline-block h-2 w-2 rounded-full"
        style={{ backgroundColor: on ? color : "transparent", border: `1px solid ${color}` }}
      />
      {label}
    </button>
  );
}

function PowerRibbon({
  powerPts,
  sx,
  cap,
  textTF,
}: {
  powerPts: { x: number; y: number }[];
  sx: (x: number) => number;
  cap: number;
  textTF: (x: number, y: number) => string;
}) {
  if (powerPts.length < 2) return null;
  return (
    <g>
      <g transform={textTF(4, RIBBON_TOP + RIBBON_H - 1)}>
        <text
          className="fill-neutral-500"
          fontSize={9}
          style={{ textTransform: "uppercase", letterSpacing: 0.6 }}
        >
          Power
        </text>
      </g>
      {/* Per-interval color cells live inside the plot clip so they
          can't bleed past the y-axis gutter when the chart is zoomed
          in. The frame rect and right-side legend stay outside the
          clip so they always render at the panel edges. */}
      <g clipPath="url(#dt-plot-clip)">
        {powerPts.slice(0, -1).map((p, i) => {
          const next = powerPts[i + 1];
          const x0 = sx(p.x);
          const x1 = sx(next.x);
          if (x1 <= x0) return null;
          const ratio = Math.max(-1, Math.min(1, p.y / cap));
          const fill =
            ratio > 0
              ? interpHex("#1f2937", SERIES.draw, ratio)
              : interpHex("#1f2937", SERIES.regen, -ratio);
          return (
            <rect
              key={`pr-${i}`}
              x={x0}
              y={RIBBON_TOP}
              width={x1 - x0}
              height={RIBBON_H}
              fill={fill}
              opacity={0.95}
            />
          );
        })}
      </g>
      <rect
        x={PAD_L}
        y={RIBBON_TOP}
        width={PLOT_W}
        height={RIBBON_H}
        fill="transparent"
        className="stroke-neutral-800"
        strokeWidth={0.5}
      />
      <g transform={textTF(VIEW_W - PAD_R - 4, RIBBON_TOP + RIBBON_H - 1)}>
        <text
          textAnchor="end"
          className="fill-neutral-600"
          fontSize={8.5}
        >
          regen ◀ • ▶ draw
        </text>
      </g>
    </g>
  );
}

function ElevationBackdrop({
  elevPts,
  sx,
  top,
  height,
}: {
  elevPts: { x: number; y: number }[];
  sx: (x: number) => number;
  top: number;
  height: number;
}) {
  if (elevPts.length < 2) return null;
  const ys = elevPts.map((p) => p.y);
  const lo = Math.min(...ys);
  const hi = Math.max(...ys);
  const span = Math.max(1e-9, hi - lo);
  // Confine to the bottom 75% of the panel so the SoC line up top
  // doesn't crash into the elevation ridge on hilly drives.
  const bandH = height * 0.75;
  const baseY = top + height;
  const proj = elevPts.map((p) => ({
    x: sx(p.x),
    y: baseY - ((p.y - lo) / span) * bandH,
  }));
  const path = monotonePath(proj);
  const last = proj[proj.length - 1];
  const first = proj[0];
  return (
    <g clipPath="url(#dt-plot-clip)">
      <path
        d={`${path} L ${last.x.toFixed(2)},${baseY.toFixed(2)} L ${first.x.toFixed(2)},${baseY.toFixed(2)} Z`}
        fill={SERIES.elevation}
        opacity={0.10}
      />
      <path
        d={path}
        fill="none"
        stroke={SERIES.elevation}
        strokeWidth={0.9}
        opacity={0.4}
        vectorEffect="non-scaling-stroke"
      />
    </g>
  );
}

function CategoricalStrip({
  title,
  top,
  height,
  bands,
  showLabels,
  textTF,
}: {
  title: string;
  top: number;
  height: number;
  bands: { x0: number; x1: number; color: string; label: string }[];
  showLabels: boolean;
  textTF: (x: number, y: number) => string;
}) {
  return (
    <g>
      <g transform={textTF(4, top + height - 2)}>
        <text className="fill-neutral-500" fontSize={9}>
          {title}
        </text>
      </g>
      <rect
        x={PAD_L}
        y={top}
        width={PLOT_W}
        height={height}
        className="fill-neutral-900"
        rx={2}
      />
      <g clipPath="url(#dt-plot-clip)">
        {bands.map((b, i) => {
          const w = Math.max(0, b.x1 - b.x0);
          return (
            <g key={`bn-${i}`}>
              <rect
                x={b.x0}
                y={top}
                width={w}
                height={height}
                fill={b.color}
                opacity={0.55}
              />
              {showLabels && w > 70 ? (
                <g transform={textTF(b.x0 + 6, top + height - 3)}>
                  <text className="fill-neutral-100" fontSize={9}>
                    {b.label}
                  </text>
                </g>
              ) : null}
              <title>{b.label}</title>
            </g>
          );
        })}
      </g>
    </g>
  );
}

function TimeAxis({
  xMin,
  xMax,
  sx,
  top,
  textTF,
}: {
  xMin: number;
  xMax: number;
  sx: (x: number) => number;
  top: number;
  textTF: (x: number, y: number) => string;
}) {
  const ticks = niceTimeTicks(xMin, xMax, 5);
  return (
    <g>
      {ticks.map((t, i) => {
        // Anchor the first and last tick labels to start/end of the
        // tick line so the label can't extend past the SVG bounds and
        // get clipped (e.g. "06:27 PM" turning into "06:27 P" at the
        // right edge). Interior ticks stay centered on the tick.
        const anchor: "start" | "middle" | "end" =
          i === 0 ? "start" : i === ticks.length - 1 ? "end" : "middle";
        return (
          <g key={`tt-${i}`}>
            <line
              x1={sx(t)}
              x2={sx(t)}
              y1={top - 4}
              y2={top - 1}
              className="stroke-neutral-700"
              strokeWidth={0.6}
            />
            <g transform={textTF(sx(t), top + 10)}>
              <text
                textAnchor={anchor}
                className="fill-neutral-500"
                fontSize={10}
                style={{ fontVariantNumeric: "tabular-nums" }}
              >
                {fmtClock(t)}
              </text>
            </g>
          </g>
        );
      })}
    </g>
  );
}

function FloatingTooltip({
  x,
  cursorSample,
  cursorPower,
  cursorSoc,
  cursorElev,
  cursorHeadwind,
  tempUnit,
}: {
  x: number;
  cursorSample: Sample;
  cursorPower: number | null;
  cursorSoc: number | null;
  cursorElev: number | null;
  cursorHeadwind: number | null;
  tempUnit: "f" | "c";
}) {
  const rows: { color: string; label: string; value: string }[] = [
    {
      color: SERIES.speed,
      label: "Speed",
      value: `${(cursorSample.SpeedMph || 0).toFixed(0)} mph`,
    },
  ];
  if (cursorPower != null) {
    rows.push({
      color: cursorPower >= 0 ? SERIES.draw : SERIES.regen,
      label: "Power",
      value:
        cursorPower >= 0
          ? `+${cursorPower.toFixed(0)} kW`
          : `${cursorPower.toFixed(0)} kW`,
    });
  }
  if (cursorSoc != null) {
    rows.push({
      color: SERIES.battery,
      label: "SoC",
      value: `${cursorSoc.toFixed(1)} %`,
    });
  }
  if (cursorElev != null) {
    rows.push({
      color: SERIES.elevation,
      label: "Elev",
      value: `${cursorElev.toFixed(0)} ft`,
    });
  }
  if (cursorSample.OutsideTempC && cursorSample.OutsideTempC !== 0) {
    rows.push({
      color: "#fb923c",
      label: "Outside",
      value: fmtTemp(cursorSample.OutsideTempC, tempUnit),
    });
  }
  if (cursorSample.pack_temp_avg_c && cursorSample.pack_temp_avg_c !== 0) {
    rows.push({
      color: "#f472b6",
      label: "Battery",
      value: fmtTemp(cursorSample.pack_temp_avg_c, tempUnit),
    });
  }
  if (cursorHeadwind != null) {
    rows.push({
      color: "#0891b2",
      label: "Wind",
      value:
        cursorHeadwind >= 0
          ? `+${cursorHeadwind.toFixed(0)} mph head`
          : `${cursorHeadwind.toFixed(0)} mph tail`,
    });
  }

  const W = 198;
  const headerH = 22;
  const rowH = 14;
  const H = headerH + rows.length * rowH + 8;
  const flip = x > VIEW_W - PAD_R - W - 12;
  const tx = flip ? x - W - 8 : x + 8;
  const ty = SPEED_TOP - 4;
  return (
    <g pointerEvents="none">
      <rect
        x={tx}
        y={ty}
        width={W}
        height={H}
        rx={6}
        fill="#0a0a0aee"
        stroke="#404040"
        strokeWidth={0.8}
      />
      <text
        x={tx + 10}
        y={ty + 14}
        className="fill-neutral-100"
        fontSize={11}
        fontWeight={600}
        style={{ fontVariantNumeric: "tabular-nums" }}
      >
        {fmtClockSec(new Date(cursorSample.At).getTime())}
      </text>
      <line
        x1={tx + 8}
        x2={tx + W - 8}
        y1={ty + headerH - 4}
        y2={ty + headerH - 4}
        className="stroke-neutral-800"
        strokeWidth={0.6}
      />
      {rows.map((r, i) => {
        const ry = ty + headerH + i * rowH + 2;
        return (
          <g key={`tt-${i}`}>
            <rect
              x={tx + 10}
              y={ry + 2}
              width={6}
              height={6}
              rx={1}
              fill={r.color}
            />
            <text
              x={tx + 22}
              y={ry + 8}
              className="fill-neutral-300"
              fontSize={10.5}
            >
              {r.label}
            </text>
            <text
              x={tx + W - 10}
              y={ry + 8}
              textAnchor="end"
              className="fill-neutral-100"
              fontSize={10.5}
              fontWeight={500}
              style={{ fontVariantNumeric: "tabular-nums" }}
            >
              {r.value}
            </text>
          </g>
        );
      })}
    </g>
  );
}

function MomentChips({
  moments,
  cursorMs,
  onCursorChange,
}: {
  moments: Moment[];
  cursorMs: number | null;
  onCursorChange: (ms: number | null) => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="text-[10px] uppercase tracking-wide text-neutral-500 mr-1">
        Moments
      </span>
      {moments.map((m, i) => {
        const active =
          cursorMs != null && Math.abs(cursorMs - m.ms) < 5_000;
        const tone = chipToneClasses(m.tone, active);
        return (
          <button
            key={i}
            type="button"
            onClick={() => onCursorChange(m.ms)}
            className={`inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] font-medium transition-colors ${tone}`}
            title={m.detail}
          >
            <span className="font-mono text-[10px] text-neutral-500">
              {fmtClock(m.ms)}
            </span>
            {m.label}
          </button>
        );
      })}
    </div>
  );
}

// CursorReadout — HTML alternative to the SVG floating tooltip.
// Renders as a compact pill-grid below the chart so it's readable at
// any SVG zoom level. On touch devices the SVG tooltip is hidden and
// this becomes the primary way to inspect a data point.
// `onDismiss` is only provided on touch (no dismiss button for mouse —
// the cursor clears automatically when the pointer leaves the chart).
function CursorReadout({
  cursorSample,
  cursorPower,
  cursorSoc,
  cursorElev,
  cursorHeadwind,
  tempUnit,
  onDismiss,
}: {
  cursorSample: Sample;
  cursorPower: number | null;
  cursorSoc: number | null;
  cursorElev: number | null;
  cursorHeadwind: number | null;
  tempUnit: "f" | "c";
  onDismiss?: () => void;
}) {
  const items: { color: string; label: string; value: string }[] = [
    {
      color: SERIES.speed,
      label: "Speed",
      value: `${(cursorSample.SpeedMph || 0).toFixed(0)} mph`,
    },
  ];
  if (cursorPower != null) {
    items.push({
      color: cursorPower >= 0 ? SERIES.draw : SERIES.regen,
      label: "Power",
      value:
        cursorPower >= 0
          ? `+${cursorPower.toFixed(0)} kW`
          : `${cursorPower.toFixed(0)} kW`,
    });
  }
  if (cursorSoc != null) {
    items.push({
      color: SERIES.battery,
      label: "SoC",
      value: `${cursorSoc.toFixed(1)} %`,
    });
  }
  if (cursorElev != null) {
    items.push({
      color: SERIES.elevation,
      label: "Elev",
      value: `${cursorElev.toFixed(0)} ft`,
    });
  }
  if (cursorSample.OutsideTempC && cursorSample.OutsideTempC !== 0) {
    items.push({
      color: "#fb923c",
      label: "Outside",
      value: fmtTemp(cursorSample.OutsideTempC, tempUnit),
    });
  }
  if (cursorSample.pack_temp_avg_c && cursorSample.pack_temp_avg_c !== 0) {
    items.push({
      color: "#f472b6",
      label: "Battery",
      value: fmtTemp(cursorSample.pack_temp_avg_c, tempUnit),
    });
  }
  if (cursorHeadwind != null) {
    items.push({
      color: "#0891b2",
      label: "Wind",
      value:
        cursorHeadwind >= 0
          ? `+${cursorHeadwind.toFixed(0)} mph head`
          : `${Math.abs(cursorHeadwind).toFixed(0)} mph tail`,
    });
  }

  return (
    <div className="flex flex-wrap items-center gap-1.5 rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-2">
      <span className="mr-1 font-mono text-[11px] tabular-nums text-neutral-400">
        {fmtClockSec(new Date(cursorSample.At).getTime())}
      </span>
      {items.map((item) => (
        <span
          key={item.label}
          className="inline-flex items-center gap-1 rounded-full border border-neutral-800 bg-neutral-900 px-2 py-0.5 text-[11px]"
        >
          <span
            className="inline-block h-2 w-2 shrink-0 rounded-xs"
            style={{ background: item.color }}
          />
          <span className="text-neutral-500">{item.label}</span>
          <span className="font-medium tabular-nums text-neutral-200">
            {item.value}
          </span>
        </span>
      ))}
      {onDismiss ? (
        <button
          type="button"
          onClick={onDismiss}
          className="ml-auto rounded-full border border-neutral-800 px-2 py-0.5 text-[11px] text-neutral-500 hover:border-neutral-700 hover:text-neutral-300"
          aria-label="Clear cursor"
        >
          ✕
        </button>
      ) : null}
    </div>
  );
}

function NoSamples() {
  return (
    <p className="text-sm text-neutral-500">
      No telemetry was recorded for this drive.
    </p>
  );
}

// ---- Helpers --------------------------------------------------------------

// Speed series: keep the existing synthetic-zero anchor so the line
// visibly returns to 0 at drives that end with the car still moving.
function buildSpeedPts(samples: Sample[]): { x: number; y: number }[] {
  const raw = samples.map((p) => ({
    x: new Date(p.At).getTime(),
    y: p.SpeedMph || 0,
  }));
  if (raw.length === 0) return raw;
  const last = raw[raw.length - 1];
  if ((last.y ?? 0) <= 0.5) return raw;
  return [...raw, { x: last.x + 1000, y: 0 }];
}

function buildModeSegments(
  samples: Sample[],
  startMs: number,
  endMs: number,
): ModeSegment[] {
  if (samples.length === 0) return [];
  const out: ModeSegment[] = [];
  for (let i = 0; i < samples.length; i++) {
    const cur = samples[i];
    const curMs = new Date(cur.At).getTime();
    const nextMs =
      i + 1 < samples.length
        ? new Date(samples[i + 1].At).getTime()
        : endMs;
    if (
      !Number.isFinite(curMs) ||
      !Number.isFinite(nextMs) ||
      nextMs <= curMs
    ) {
      continue;
    }
    const mode = classifyMode(cur);
    const seg: ModeSegment = {
      x0: Math.max(curMs, startMs),
      x1: Math.min(nextMs, endMs),
      mode,
    };
    if (seg.x1 <= seg.x0) continue;
    const prev = out[out.length - 1];
    if (prev && prev.mode === mode && Math.abs(prev.x1 - seg.x0) <= 1500) {
      prev.x1 = seg.x1;
    } else {
      out.push(seg);
    }
  }
  return out;
}

// buildParkBands shades the windows where the vehicle wasn't driving
// (shift_state D/R) for ≥ 5 min. Rivian stops sending telemetry when
// the car is asleep, so absence of D/R samples is the real signal —
// raw speed reads ~4 mph at idle and never crosses zero, and only a
// handful of P samples land per trip.
//
// A band is classified as a true "parked" stretch only if both
// bounding samples are stationary (≤ 5 mph) AND their GPS coords
// barely moved (< 150 m). Otherwise the band is a telemetry "gap" —
// the car was driving but the recorder lost samples (cell dead zone,
// subscription churn, gateway throttling).
function buildParkBands(
  samples: Sample[],
  startMs: number,
  endMs: number,
): ParkBand[] {
  // Two thresholds: a long pause to read as Parked, a shorter one
  // to flag a telemetry gap mid-drive. Anything below GAP_MIN_MS is
  // ignored — the normal D-to-D cadence has occasional 30-60s holes
  // that aren't worth shading.
  const PARK_MIN_MS = 5 * 60_000;
  const GAP_MIN_MS = 90_000; // 90s gap mid-drive becomes a band
  const SPEED_PARK_MAX = 5; // mph
  const DIST_PARK_MAX_M = 150;
  const out: ParkBand[] = [];
  type Marker = { ms: number; s: Sample };
  const driving: Marker[] = [];
  for (const s of samples) {
    const g = (s.ShiftState ?? "").trim().toUpperCase();
    if (g !== "D" && g !== "R" && g !== "DRIVE" && g !== "REVERSE") continue;
    const ms = new Date(s.At).getTime();
    if (Number.isFinite(ms)) driving.push({ ms, s });
  }
  driving.sort((a, b) => a.ms - b.ms);
  const haversineM = (a: Sample, b: Sample): number => {
    if (
      !Number.isFinite(a.Lat) ||
      !Number.isFinite(a.Lon) ||
      !Number.isFinite(b.Lat) ||
      !Number.isFinite(b.Lon)
    )
      return Infinity;
    const R = 6371000;
    const f1 = (a.Lat * Math.PI) / 180;
    const f2 = (b.Lat * Math.PI) / 180;
    const df = ((b.Lat - a.Lat) * Math.PI) / 180;
    const dl = ((b.Lon - a.Lon) * Math.PI) / 180;
    const x =
      Math.sin(df / 2) ** 2 + Math.cos(f1) * Math.cos(f2) * Math.sin(dl / 2) ** 2;
    return 2 * R * Math.asin(Math.sqrt(x));
  };
  // A band is "parked" only when both bounding samples are stationary
  // and the GPS coords barely moved. Otherwise it's a telemetry hole.
  // Missing endpoints (leading/trailing band) default to parked because
  // we can't prove the car was moving across an open boundary.
  const classify = (
    a: Sample | null,
    b: Sample | null,
  ): "parked" | "gap" => {
    if (!a && !b) return "parked";
    const aMoving = a ? Math.abs(a.SpeedMph ?? 0) > SPEED_PARK_MAX : false;
    const bMoving = b ? Math.abs(b.SpeedMph ?? 0) > SPEED_PARK_MAX : false;
    if (aMoving || bMoving) return "gap";
    if (a && b && haversineM(a, b) > DIST_PARK_MAX_M) return "gap";
    return "parked";
  };
  const push = (x0: number, x1: number, kind: "parked" | "gap") => {
    const minMs = kind === "parked" ? PARK_MIN_MS : GAP_MIN_MS;
    if (x1 - x0 >= minMs) out.push({ x0, x1, kind });
  };
  if (driving.length === 0) {
    push(startMs, endMs, "parked");
    return out;
  }
  push(startMs, driving[0].ms, classify(null, driving[0].s));
  for (let i = 1; i < driving.length; i++) {
    push(
      driving[i - 1].ms,
      driving[i].ms,
      classify(driving[i - 1].s, driving[i].s),
    );
  }
  const tail = driving[driving.length - 1];
  push(tail.ms, endMs, classify(tail.s, null));
  return out;
}

function classifyMode(s: Sample): Mode {
  const raw = (s.drive_mode || "").trim().toLowerCase();
  if (raw) {
    // Rivian's wire values are "everyday" / "sport" / "distance"; the
    // formatted labels ("All-Purpose" / "Conserve") also flow through
    // here when a sample carries a display-typed value, so accept both.
    if (raw === "sport" || raw.includes("launch")) return "Sport";
    if (raw === "distance" || raw.includes("conserve") || raw.includes("eco"))
      return "Conserve";
    if (
      raw === "everyday" ||
      raw.includes("all purpose") ||
      raw.includes("all-purpose")
    )
      return "AllPurpose";
    return "Other";
  }
  // Fall back to gear so historical drives still get *some* lane data.
  const g = (s.ShiftState || "").trim().toUpperCase();
  if (g === "D" || g === "DRIVE") return "AllPurpose";
  if (g === "R" || g === "REVERSE") return "Other";
  return "Other";
}

function buildPrecipBands(
  weatherPts: DriveWeatherSamplePoint[],
  startMs: number,
  endMs: number,
): PrecipBand[] {
  // Stricter painting: require actual precipitation (>0) to draw a
  // band, even when the WMO code says "thunderstorm" or similar.
  // Open-Meteo's hourly weather_code flags convective conditions
  // (95/96/99) for any forecast hour with atmospheric instability,
  // including hours where no rain reaches the ground at the coarse
  // grid cell — that produced spurious red bands on dry drives.
  return weatherPts
    .map((p): PrecipBand | null => {
      const amt = p.precip_in ?? 0;
      if (amt <= 0) return null;
      const cond = p.conditions;
      const color = precipColor(cond) ?? SERIES.rain;
      const sStart = new Date(p.at).getTime();
      const sEnd = sStart + (p.cadence_minutes || 60) * 60_000;
      const x0 = Math.max(sStart, startMs);
      const x1 = Math.min(sEnd, endMs);
      if (x1 <= x0) return null;
      const label = `${cond ?? "precipitation"} (${amt.toFixed(2)}″)`;
      return { x0, x1, color, label };
    })
    .filter((b): b is PrecipBand => b != null);
}

function precipColor(cond: string | undefined): string | null {
  switch (cond) {
    case "rain":
      return SERIES.rain;
    case "drizzle":
      return SERIES.drizzle;
    case "snow":
      return SERIES.snow;
    case "freezing rain":
      return SERIES.freezingRain;
    case "thunderstorm":
      return SERIES.thunderstorm;
    default:
      return null;
  }
}

function buildHeadwindPts(
  weatherPts: DriveWeatherSamplePoint[],
  startMs: number,
  endMs: number,
): { x: number; y: number }[] {
  const raw = weatherPts
    .filter((p) => p.headwind_mph != null)
    .map((p) => ({
      x: new Date(p.at).getTime(),
      y: p.headwind_mph as number,
    }));
  if (raw.length === 0) return [];
  // Clamp to the drive window with step-extension at the boundaries —
  // identical reasoning to clampToDrive in the old page-level code.
  const inRange = raw.filter((p) => p.x >= startMs && p.x <= endMs);
  const before = raw.filter((p) => p.x < startMs);
  const after = raw.filter((p) => p.x > endMs);
  const startY =
    inRange[0]?.y ?? before[before.length - 1]?.y ?? after[0]?.y;
  const endY =
    inRange[inRange.length - 1]?.y ??
    after[0]?.y ??
    before[before.length - 1]?.y;
  if (startY == null || endY == null) return [];
  const out: { x: number; y: number }[] = [{ x: startMs, y: startY }];
  for (const p of inRange) {
    if (p.x > startMs && p.x < endMs) out.push(p);
  }
  out.push({ x: endMs, y: endY });
  return out;
}

// ---- Moment detection ----------------------------------------------------

function detectMoments(
  speedPts: { x: number; y: number }[],
  socPts: { x: number; y: number }[],
  powerPts: { x: number; y: number }[],
  headwindPts: { x: number; y: number }[],
): Moment[] {
  const out: Moment[] = [];

  // 1. Highway entry: first crossing from <40 mph to ≥55 mph that
  //    persists for ≥30 s.
  for (let i = 1; i < speedPts.length; i++) {
    if (speedPts[i - 1].y < 40 && speedPts[i].y >= 55) {
      let sustained = true;
      for (let j = i; j < speedPts.length; j++) {
        if (speedPts[j].x - speedPts[i].x > 30_000) break;
        if (speedPts[j].y < 50) {
          sustained = false;
          break;
        }
      }
      if (sustained) {
        out.push({
          ms: speedPts[i].x,
          label: "Highway entry",
          detail: `Crossed 55 mph and sustained for 30 s`,
          tone: "info",
        });
        break;
      }
    }
  }

  // 2. Hard brake: most negative dSpeed/dt in mph/s, only if ≤ −5.
  let worstBrake: { ms: number; rate: number } | null = null;
  for (let i = 1; i < speedPts.length; i++) {
    const dt = (speedPts[i].x - speedPts[i - 1].x) / 1000;
    if (dt <= 0 || dt > 10) continue;
    const dv = speedPts[i].y - speedPts[i - 1].y;
    const rate = dv / dt;
    if (rate < -5 && (!worstBrake || rate < worstBrake.rate)) {
      worstBrake = { ms: speedPts[i].x, rate };
    }
  }
  if (worstBrake) {
    out.push({
      ms: worstBrake.ms,
      label: "Hard brake",
      detail: `${worstBrake.rate.toFixed(1)} mph/s deceleration`,
      tone: "warning",
    });
  }

  // 3. Top speed.
  if (speedPts.length > 0) {
    let best = speedPts[0];
    for (const p of speedPts) {
      if (p.y > best.y) best = p;
    }
    if (best.y >= 50) {
      out.push({
        ms: best.x,
        label: "Top speed",
        detail: `${best.y.toFixed(0)} mph`,
        tone: "info",
      });
    }
  }

  // 4. Max regen: most negative power point. Only emit when meaningfully
  //    negative (< -20 kW) so quiet drives don't get a false marker.
  if (powerPts.length > 0) {
    let best = powerPts[0];
    for (const p of powerPts) {
      if (p.y < best.y) best = p;
    }
    if (best.y < -20) {
      out.push({
        ms: best.x,
        label: "Max regen",
        detail: `${best.y.toFixed(0)} kW recovered`,
        tone: "success",
      });
    }
  }

  // 5. Lowest SoC: only emit on drives long enough that the minimum
  //    isn't simply the last sample (i.e. the user explicitly cares
  //    that they dipped to a low point and recovered via regen later).
  if (socPts.length > 4) {
    let bestIdx = 0;
    for (let i = 1; i < socPts.length; i++) {
      if (socPts[i].y < socPts[bestIdx].y) bestIdx = i;
    }
    if (bestIdx < socPts.length - 1) {
      out.push({
        ms: socPts[bestIdx].x,
        label: "Lowest SoC",
        detail: `${socPts[bestIdx].y.toFixed(1)} %`,
        tone: "neutral",
      });
    }
  }

  // 6. Headwind peak: most positive (most into-the-wind) sample,
  //    when the absolute headwind exceeds 8 mph so cabin-side fans
  //    aren't tagged.
  if (headwindPts.length > 0) {
    let best = headwindPts[0];
    for (const p of headwindPts) {
      if (Math.abs(p.y) > Math.abs(best.y)) best = p;
    }
    if (Math.abs(best.y) >= 8) {
      out.push({
        ms: best.x,
        label: best.y >= 0 ? "Headwind peak" : "Tailwind peak",
        detail:
          best.y >= 0
            ? `+${best.y.toFixed(0)} mph head`
            : `${best.y.toFixed(0)} mph tail`,
        tone: "neutral",
      });
    }
  }

  // Eat near-duplicates: if two moments land within 8 s of each other,
  // keep the higher-tone one.
  out.sort((a, b) => a.ms - b.ms);
  const tonePriority: Record<Moment["tone"], number> = {
    warning: 4,
    success: 3,
    info: 2,
    neutral: 1,
  };
  const dedup: Moment[] = [];
  for (const m of out) {
    const prev = dedup[dedup.length - 1];
    if (prev && m.ms - prev.ms < 8000) {
      if (tonePriority[m.tone] > tonePriority[prev.tone]) {
        dedup[dedup.length - 1] = m;
      }
      continue;
    }
    dedup.push(m);
  }
  return dedup;
}

// ---- Formatting / math helpers -------------------------------------------

function fmtClock(ms: number): string {
  return new Date(ms).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  });
}

function fmtClockSec(ms: number): string {
  return new Date(ms).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function fmtTemp(c: number, unit: "f" | "c"): string {
  if (unit === "f") return `${(c * 1.8 + 32).toFixed(0)}°F`;
  return `${c.toFixed(0)}°C`;
}

function ySolve(
  v: number,
  yMin: number,
  yMax: number,
  top: number,
  height: number,
): number {
  if (yMax === yMin) return top + height / 2;
  return top + height - ((v - yMin) / (yMax - yMin)) * height;
}

function nearestY(
  pts: { x: number; y: number }[],
  x: number | null,
): number | null {
  if (x == null || pts.length === 0) return null;
  let best = pts[0];
  let bestD = Math.abs(best.x - x);
  for (let i = 1; i < pts.length; i++) {
    const d = Math.abs(pts[i].x - x);
    if (d < bestD) {
      bestD = d;
      best = pts[i];
    }
  }
  return best.y;
}

function snapToSample(
  x: number,
  pts: { x: number; y: number }[],
): number {
  if (pts.length === 0) return x;
  let best = pts[0].x;
  let bestD = Math.abs(pts[0].x - x);
  for (let i = 1; i < pts.length; i++) {
    const d = Math.abs(pts[i].x - x);
    if (d < bestD) {
      bestD = d;
      best = pts[i].x;
    }
  }
  return best;
}

function niceTimeTicks(min: number, max: number, n: number): number[] {
  if (n <= 1) return [min];
  const span = max - min;
  const step = span / (n - 1);
  const out: number[] = [];
  for (let i = 0; i < n; i++) out.push(min + step * i);
  return out;
}

function momentColor(tone: Moment["tone"]): string {
  switch (tone) {
    case "warning":
      return "#f59e0b";
    case "success":
      return "#22c55e";
    case "info":
      return "#38bdf8";
    default:
      return "#a3a3a3";
  }
}

function chipToneClasses(tone: Moment["tone"], active: boolean): string {
  if (active) {
    return "bg-emerald-900/40 border-emerald-700 text-emerald-200";
  }
  switch (tone) {
    case "warning":
      return "bg-amber-950/40 border-amber-900 text-amber-200 hover:bg-amber-900/40";
    case "success":
      return "bg-emerald-950/40 border-emerald-900 text-emerald-200 hover:bg-emerald-900/40";
    case "info":
      return "bg-sky-950/40 border-sky-900 text-sky-200 hover:bg-sky-900/40";
    default:
      return "bg-neutral-900 border-neutral-800 text-neutral-300 hover:bg-neutral-800";
  }
}

function interpHex(a: string, b: string, t: number): string {
  const ar = parseInt(a.slice(1, 3), 16);
  const ag = parseInt(a.slice(3, 5), 16);
  const ab = parseInt(a.slice(5, 7), 16);
  const br = parseInt(b.slice(1, 3), 16);
  const bg = parseInt(b.slice(3, 5), 16);
  const bb = parseInt(b.slice(5, 7), 16);
  const r = Math.round(ar + (br - ar) * t);
  const g = Math.round(ag + (bg - ag) * t);
  const bl = Math.round(ab + (bb - ab) * t);
  return `#${pad2(r)}${pad2(g)}${pad2(bl)}`;
}

function pad2(n: number): string {
  return n.toString(16).padStart(2, "0");
}

// ---- Path interpolation --------------------------------------------------

function linePath(pts: { x: number; y: number }[]): string {
  if (pts.length === 0) return "";
  let d = `M ${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}`;
  for (let i = 1; i < pts.length; i++) {
    d += ` L ${pts[i].x.toFixed(2)},${pts[i].y.toFixed(2)}`;
  }
  return d;
}

// Fritsch–Carlson monotone cubic — preserves local extrema (no overshoot).
function monotonePath(pts: { x: number; y: number }[]): string {
  const n = pts.length;
  if (n < 2) return linePath(pts);
  const dx = new Array<number>(n - 1);
  const dy = new Array<number>(n - 1);
  const m = new Array<number>(n - 1);
  for (let i = 0; i < n - 1; i++) {
    dx[i] = pts[i + 1].x - pts[i].x;
    dy[i] = pts[i + 1].y - pts[i].y;
    m[i] = dx[i] === 0 ? 0 : dy[i] / dx[i];
  }
  const t = new Array<number>(n);
  t[0] = m[0];
  t[n - 1] = m[n - 2];
  for (let i = 1; i < n - 1; i++) {
    if (m[i - 1] * m[i] <= 0) {
      t[i] = 0;
    } else {
      t[i] = (m[i - 1] + m[i]) / 2;
      const a = t[i] / m[i - 1];
      const b = t[i] / m[i];
      const h = a * a + b * b;
      if (h > 9) {
        const tau = 3 / Math.sqrt(h);
        t[i] = tau * m[i - 1] * a;
      }
    }
  }
  let d = `M ${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}`;
  for (let i = 0; i < n - 1; i++) {
    const h = dx[i];
    const c1x = pts[i].x + h / 3;
    const c1y = pts[i].y + (t[i] * h) / 3;
    const c2x = pts[i + 1].x - h / 3;
    const c2y = pts[i + 1].y - (t[i + 1] * h) / 3;
    d += ` C ${c1x.toFixed(2)},${c1y.toFixed(2)} ${c2x.toFixed(2)},${c2y.toFixed(2)} ${pts[i + 1].x.toFixed(2)},${pts[i + 1].y.toFixed(2)}`;
  }
  return d;
}
