// DriveTimeline: stacked small multiples that share one time axis and one
// synced scrub cursor.
//
//   Speed (mph)      the tallest panel, gradient fill, moment flags
//   Power (kW)       signed area around 0: draw above, regen below
//   Battery (%)      SoC line with elevation (ft) as a faint backdrop
//   Pack temp        optional, off by default
//
// One readout row above the panels replaces the floating tooltip: with a
// cursor it shows the values at that moment, without one a drive summary.
// The mode band appears only when the drive-mode changes, and the
// precipitation row only when there is precipitation. Time ticks sit on
// round clock times in the display zone.
//
// Interaction: hover (mouse) or touch-drag scrubs. A mouse drag selects a
// zoom window; double-click resets it. A plain vertical swipe on touch
// still scrolls the page (touch-action: pan-y).

import { useLayoutEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import type { Drive, DriveWeatherSamplePoint, Sample } from "../lib/api";
import {
  buildHeadwindPts,
  buildModeSegments,
  buildParkBands,
  buildPrecipBands,
  buildSpeedPts,
  buildTimeScale,
  detectMoments,
  fmtTemp,
  MODE_BAND,
  MODE_LABEL,
  momentColor,
  monotonePath,
  nearestY,
  niceTimeTicks,
  snapToSample,
  type Moment,
} from "../lib/driveTimelineData";
import { displayTimeZone } from "../lib/format";
import { buildElevPts, derivePower } from "../lib/power";
import { smoothGaussianTime } from "../lib/smooth";

export type DriveTimelineProps = {
  drive: Drive;
  samples: Sample[];
  weatherPts: DriveWeatherSamplePoint[];
  cursorMs: number | null;
  onCursorChange: (ms: number | null) => void;
  // Controlled zoom window in epoch ms. `null` means the full drive.
  // Lifted to the page so the route map can crop to the same window.
  viewWindow: [number, number] | null;
  onViewWindowChange: (window: [number, number] | null) => void;
  tempUnit: "f" | "c";
};

type Pt = { x: number; y: number };

// Theme tokens: classes flip with the light theme (the palette variables
// are remapped under html[data-theme=light]); no hard-coded greys.
const C = {
  speed: "var(--color-sky-500)",
  draw: "var(--color-amber-500)",
  regen: "var(--color-emerald-500)",
  soc: "var(--color-emerald-500)",
  temp: "var(--color-rose-500)",
  elev: "var(--color-neutral-500)",
};

const PAD_L = 46;
const PAD_T = 20;
const PANEL_GAP = 26;
const AXIS_H = 22;
const DEAD_PX = 26;

function clockLabel(ms: number, withSeconds = false): string {
  return new Date(ms).toLocaleTimeString(undefined, {
    hour: "numeric",
    minute: "2-digit",
    second: withSeconds ? "2-digit" : undefined,
    timeZone: displayTimeZone(),
  });
}

// niceAxis picks a round step so the axis has at most `maxTicks` ticks
// and tops out just above the data (73 mph -> 0/20/40/60/80).
function niceAxis(max: number, maxTicks = 5, steps = [5, 10, 20, 25, 50, 100]): { top: number; ticks: number[] } {
  const step = steps.find((s) => Math.ceil(max / s) + 1 <= maxTicks) ?? steps[steps.length - 1];
  const top = Math.max(step, Math.ceil(max / step) * step);
  const ticks: number[] = [];
  for (let v = 0; v <= top + 1e-9; v += step) ticks.push(v);
  return { top, ticks };
}

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
  const [showPackTemp, setShowPackTemp] = useState(false);
  const [showWind, setShowWind] = useState(false);
  const [isTouch, setIsTouch] = useState(false);
  const driveStartMs = new Date(drive.StartedAt).getTime();
  const driveEndMs = new Date(drive.EndedAt).getTime();

  const speedPts = useMemo(() => buildSpeedPts(samples), [samples]);
  const socPts = useMemo(
    () =>
      smoothGaussianTime(
        // A missing SoC is not 0 %: drop null / non-positive rows so they
        // don't plot a plunge or a false "lowest SoC 0 %" moment.
        samples
          .filter((p) => typeof p.BatteryLevelPct === "number" && p.BatteryLevelPct > 0)
          .map((p) => ({ x: new Date(p.At).getTime(), y: p.BatteryLevelPct })),
        30_000,
      ),
    [samples],
  );
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
  const packTempPts = useMemo(() => {
    const raw = samples
      .filter((p) => p.pack_temp_avg_c != null && p.pack_temp_avg_c !== 0)
      .map((p) => ({ x: new Date(p.At).getTime(), y: p.pack_temp_avg_c as number }));
    return raw.length > 1 ? smoothGaussianTime(raw, 60_000) : raw;
  }, [samples]);
  const moments = useMemo(
    () => detectMoments(speedPts, socPts, powerPts, headwindPts),
    [speedPts, socPts, powerPts, headwindPts],
  );
  const distinctModes = useMemo(() => new Set(modeSegments.map((s) => s.mode)), [modeSegments]);

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

  if (speedPts.length === 0 && socPts.length === 0) return <NoSamples />;

  const allX = [...speedPts.map((p) => p.x), ...socPts.map((p) => p.x)];
  const fullMin = allX.length > 0 ? Math.min(...allX) : driveStartMs;
  const fullMax = allX.length > 0 ? Math.max(...allX) : driveEndMs;
  const xMin = viewWindow ? Math.max(fullMin, Math.min(viewWindow[0], viewWindow[1])) : fullMin;
  const xMax = viewWindow ? Math.min(fullMax, Math.max(viewWindow[0], viewWindow[1])) : fullMax;
  const isZoomed = viewWindow != null;

  const hasPower = powerPts.length > 1;
  const hasElev = elevPts.length > 1;
  const hasTemp = packTempPts.length > 1;
  const hasWind = headwindPts.length > 1;

  return (
    <div className="space-y-2">
      <Readout
        cursorSample={cursorSample}
        cursorMs={cursorMs}
        power={nearestY(powerPts, cursorMs)}
        soc={nearestY(socPts, cursorMs)}
        elev={nearestY(elevPts, cursorMs)}
        wind={nearestY(headwindPts, cursorMs)}
        showTemp={showPackTemp}
        tempUnit={tempUnit}
        drive={drive}
        onDismiss={isTouch ? () => onCursorChange(null) : undefined}
      />

      <Panels
        xMin={xMin}
        xMax={xMax}
        speedPts={speedPts}
        powerPts={hasPower ? powerPts : []}
        socPts={socPts}
        elevPts={hasElev ? elevPts : []}
        packTempPts={showPackTemp && hasTemp ? packTempPts : []}
        headwindPts={showWind && hasWind ? headwindPts : []}
        tempUnit={tempUnit}
        parkBands={parkBands}
        modeSegments={distinctModes.size > 1 ? modeSegments : []}
        precipBands={precipBands}
        moments={moments}
        cursorMs={cursorMs}
        onCursorChange={onCursorChange}
        onViewWindowChange={onViewWindowChange}
        onPointerTypeChange={setIsTouch}
        isZoomed={isZoomed}
        sampleXs={speedPts}
      />

      {distinctModes.size > 1 ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-neutral-500">
          <span>Drive mode</span>
          {[...distinctModes].map((m) => (
            <span key={m} className="inline-flex items-center gap-1">
              <span className="inline-block h-2 w-2 rounded-xs" style={{ background: MODE_BAND[m] }} />
              {MODE_LABEL[m]}
            </span>
          ))}
        </div>
      ) : distinctModes.size === 1 ? (
        <div className="text-[11px] text-neutral-500">
          Drive mode: {MODE_LABEL[[...distinctModes][0]]}
        </div>
      ) : null}

      <div className="flex items-center justify-between gap-3">
        {moments.length > 0 ? (
          <MomentChips
            moments={moments}
            cursorMs={cursorMs}
            onCursorChange={(ms) => {
              onCursorChange(ms);
              if (ms != null && (ms < xMin || ms > xMax)) onViewWindowChange(null);
            }}
          />
        ) : (
          <span />
        )}
        <div className="flex shrink-0 items-center gap-2">
          {hasTemp ? (
            <button
              type="button"
              aria-pressed={showPackTemp}
              onClick={() => setShowPackTemp((v) => !v)}
              className={`rounded-full border px-2.5 py-0.5 text-[11px] ${
                showPackTemp
                  ? "border-neutral-600 bg-neutral-800 text-neutral-100"
                  : "border-neutral-800 text-neutral-400 hover:text-neutral-200"
              }`}
            >
              Pack temp
            </button>
          ) : null}
          {hasWind ? (
            <button
              type="button"
              aria-pressed={showWind}
              onClick={() => setShowWind((v) => !v)}
              className={`rounded-full border px-2.5 py-0.5 text-[11px] ${
                showWind
                  ? "border-neutral-600 bg-neutral-800 text-neutral-100"
                  : "border-neutral-800 text-neutral-400 hover:text-neutral-200"
              }`}
            >
              Headwind
            </button>
          ) : null}
          {isZoomed ? (
            <button
              type="button"
              onClick={() => onViewWindowChange(null)}
              className="inline-flex items-center gap-1.5 rounded-full border border-neutral-700 bg-neutral-900 px-2.5 py-0.5 text-[11px] font-medium text-neutral-300 hover:bg-neutral-800"
              title="Reset chart zoom (also re-fits the route map)"
            >
              <span aria-hidden>⤢</span>
              Reset zoom
            </button>
          ) : (
            <span className="hidden text-[10px] text-neutral-600 sm:inline">
              Hover to scrub · drag to zoom · double-click to reset
            </span>
          )}
        </div>
      </div>
      {isTouch && !isZoomed ? <p className="text-[10px] text-neutral-600 sm:hidden">Drag to scrub</p> : null}
    </div>
  );
}

// ---- Panels ---------------------------------------------------------------

function Panels(props: {
  xMin: number;
  xMax: number;
  speedPts: Pt[];
  powerPts: Pt[];
  socPts: Pt[];
  elevPts: Pt[];
  packTempPts: Pt[];
  headwindPts: Pt[];
  tempUnit: "f" | "c";
  parkBands: ReturnType<typeof buildParkBands>;
  modeSegments: ReturnType<typeof buildModeSegments>;
  precipBands: ReturnType<typeof buildPrecipBands>;
  moments: Moment[];
  cursorMs: number | null;
  onCursorChange: (ms: number | null) => void;
  onViewWindowChange: (w: [number, number] | null) => void;
  onPointerTypeChange: (touch: boolean) => void;
  isZoomed: boolean;
  sampleXs: Pt[];
}) {
  const {
    xMin,
    xMax,
    speedPts,
    powerPts,
    socPts,
    elevPts,
    packTempPts,
    headwindPts,
    tempUnit,
    parkBands,
    modeSegments,
    precipBands,
    moments,
    cursorMs,
    onCursorChange,
    onViewWindowChange,
    onPointerTypeChange,
    sampleXs,
  } = props;

  const wrapRef = useRef<HTMLDivElement | null>(null);
  const [width, setWidth] = useState(640);
  useLayoutEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const measure = () => setWidth(Math.max(260, Math.round(el.getBoundingClientRect().width)));
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const narrow = width < 480;
  const padR = elevPts.length > 1 ? 40 : 10;
  const plotW = Math.max(120, width - PAD_L - padR);
  const scale = useMemo(
    () => buildTimeScale(xMin, xMax, parkBands, PAD_L, plotW, DEAD_PX),
    [xMin, xMax, parkBands, plotW],
  );
  const sx = scale.sx;

  // Panel stack. Heights are in px; the speed panel is the tallest.
  const speedH = narrow ? 120 : 150;
  const powerH = narrow ? 64 : 76;
  const battH = narrow ? 80 : 96;
  const tempH = 56;
  const windH = 56;
  let y = PAD_T;
  const speedTop = y;
  y += speedH + PANEL_GAP;
  const powerTop = y;
  if (powerPts.length > 1) y += powerH + PANEL_GAP;
  const battTop = y;
  y += battH + PANEL_GAP;
  const tempTop = y;
  if (packTempPts.length > 1) y += tempH + PANEL_GAP;
  const windTop = y;
  if (headwindPts.length > 1) y += windH + PANEL_GAP;
  const stripsTop = y - PANEL_GAP + 8;
  let sy = stripsTop;
  const modeTop = sy;
  if (modeSegments.length > 0) sy += 6 + 4;
  const precipTop = sy;
  if (precipBands.length > 0) sy += 8 + 4;
  const axisTop = sy + 2;
  const totalH = axisTop + AXIS_H;
  const plotBottom = stripsTop - 8;

  // y domains and ticks
  const { top: speedMax, ticks: speedTicks } = niceAxis(Math.max(20, ...speedPts.map((p) => p.y)));
  const pMax = Math.max(10, ...powerPts.map((p) => p.y));
  const pMin = Math.min(-10, ...powerPts.map((p) => p.y));
  const pStep = pMax - pMin > 150 ? 50 : 25;
  const pHi = Math.ceil(pMax / pStep) * pStep;
  const pLo = Math.floor(pMin / pStep) * pStep;
  const powerTicks = Array.from(new Set([pLo, 0, pHi]));
  const sMinRaw = Math.min(...socPts.map((p) => p.y));
  const sMaxRaw = Math.max(...socPts.map((p) => p.y));
  const sLo = Math.max(0, Math.floor((sMinRaw - 2) / 10) * 10);
  const sHi = Math.min(100, Math.max(sLo + 20, Math.ceil((sMaxRaw + 2) / 10) * 10));
  const socTicks = [sLo, Math.round((sLo + sHi) / 2), sHi];
  const eMin = elevPts.length > 1 ? Math.min(...elevPts.map((p) => p.y)) : 0;
  const eMax = elevPts.length > 1 ? Math.max(...elevPts.map((p) => p.y)) : 1;
  const eSpan = Math.max(1, eMax - eMin);
  const eLo = eMin - eSpan * 0.1;
  const eHi = eMax + eSpan * 0.3;
  const eStep = [50, 100, 200, 250, 500, 1000, 2000, 5000].find((st) => Math.floor(eHi / st) - Math.ceil(eLo / st) + 1 <= 3) ?? 5000;
  const elevTicks: number[] = [];
  if (elevPts.length > 1) for (let v = Math.ceil(eLo / eStep) * eStep; v <= eHi; v += eStep) elevTicks.push(v);
  const wAbs = Math.max(5, ...headwindPts.map((p) => Math.abs(p.y)));
  const wTop = niceAxis(wAbs, 3, [5, 10, 20, 25, 50]).top;
  const toTemp = (c: number) => (tempUnit === "f" ? c * 1.8 + 32 : c);
  const tempPts = packTempPts.map((p) => ({ x: p.x, y: toTemp(p.y) }));
  const tMinRaw = tempPts.length ? Math.min(...tempPts.map((p) => p.y)) : 0;
  const tMaxRaw = tempPts.length ? Math.max(...tempPts.map((p) => p.y)) : 1;
  const tLo = Math.floor(tMinRaw - 1);
  const tHi = Math.max(tLo + 4, Math.ceil(tMaxRaw + 1));

  const ticks = niceTimeTicks(xMin, xMax, narrow ? 4 : 7, displayTimeZone());

  const yIn = (v: number, lo: number, hi: number, top: number, h: number) =>
    hi === lo ? top + h / 2 : top + h - ((v - lo) / (hi - lo)) * h;
  const toPath = (pts: Pt[], lo: number, hi: number, top: number, h: number) =>
    monotonePath(pts.filter((p) => p.x >= xMin && p.x <= xMax).map((p) => ({ x: sx(p.x), y: yIn(p.y, lo, hi, top, h) })));
  const areaPath = (pts: Pt[], lo: number, hi: number, top: number, h: number, baseV: number, clamp?: "pos" | "neg") => {
    const vis = pts.filter((p) => p.x >= xMin && p.x <= xMax);
    if (vis.length < 2) return "";
    const base = yIn(baseV, lo, hi, top, h);
    const pp = vis.map((p) => {
      const v = clamp === "pos" ? Math.max(p.y, baseV) : clamp === "neg" ? Math.min(p.y, baseV) : p.y;
      return { x: sx(p.x), y: yIn(v, lo, hi, top, h) };
    });
    return `M${pp[0].x},${base} L${pp.map((p) => `${p.x},${p.y}`).join(" L")} L${pp[pp.length - 1].x},${base} Z`;
  };

  // Pointer handling
  const svgRef = useRef<SVGSVGElement | null>(null);
  const [brush, setBrush] = useState<{ x0: number; x1: number } | null>(null);
  const dragRef = useRef<{ startX: number; moved: boolean; mouse: boolean } | null>(null);
  const localX = (e: ReactPointerEvent) => {
    const r = svgRef.current!.getBoundingClientRect();
    return e.clientX - r.left;
  };
  const msAt = (px: number) => scale.invert(Math.min(PAD_L + plotW, Math.max(PAD_L, px)));
  const scrubTo = (px: number) => onCursorChange(snapToSample(msAt(px), sampleXs));

  const onDown = (e: ReactPointerEvent<SVGRectElement>) => {
    const touch = e.pointerType !== "mouse";
    onPointerTypeChange(touch);
    dragRef.current = { startX: localX(e), moved: false, mouse: !touch };
    (e.currentTarget as Element).setPointerCapture?.(e.pointerId);
    if (touch) scrubTo(localX(e));
  };
  const onMove = (e: ReactPointerEvent<SVGRectElement>) => {
    const px = localX(e);
    const d = dragRef.current;
    if (d && d.mouse) {
      if (Math.abs(px - d.startX) > 5) d.moved = true;
      if (d.moved) setBrush({ x0: d.startX, x1: px });
      return;
    }
    // touch drag, or mouse hover
    if (d || e.pointerType === "mouse") scrubTo(px);
  };
  const onUp = (e: ReactPointerEvent<SVGRectElement>) => {
    const d = dragRef.current;
    dragRef.current = null;
    (e.currentTarget as Element).releasePointerCapture?.(e.pointerId);
    if (d && d.mouse && d.moved && brush) {
      const a = msAt(Math.min(brush.x0, brush.x1));
      const b = msAt(Math.max(brush.x0, brush.x1));
      setBrush(null);
      if (b - a > 20_000) onViewWindowChange([a, b]);
      return;
    }
    setBrush(null);
    if (d && d.mouse && !d.moved) scrubTo(localX(e));
  };

  const cx = cursorMs != null && cursorMs >= xMin && cursorMs <= xMax ? sx(cursorMs) : null;
  const dot = (pts: Pt[], lo: number, hi: number, top: number, h: number, color: string) => {
    if (cx == null || cursorMs == null || pts.length === 0) return null;
    const v = nearestY(pts, cursorMs);
    if (v == null) return null;
    return <circle cx={cx} cy={yIn(v, lo, hi, top, h)} r={3.5} style={{ fill: color }} className="stroke-neutral-950" strokeWidth={1.5} />;
  };

  const flags = moments.filter((m) => m.ms >= xMin && m.ms <= xMax);

  const PanelFrame = ({ top, h, title, unit, tks, lo, hi, fmt }: { top: number; h: number; title: string; unit: string; tks: number[]; lo: number; hi: number; fmt?: (v: number) => string }) => (
    <g>
      {tks.map((tv) => (
        <g key={`${title}-${tv}`}>
          <line
            x1={PAD_L}
            x2={PAD_L + plotW}
            y1={yIn(tv, lo, hi, top, h)}
            y2={yIn(tv, lo, hi, top, h)}
            className="stroke-neutral-800"
            strokeWidth={0.8}
            strokeDasharray={tv === 0 && title === "Power" ? undefined : "2 4"}
          />
          <text
            x={PAD_L - 6}
            y={Math.min(top + h, Math.max(top + 8, yIn(tv, lo, hi, top, h) + 3.5))}
            textAnchor="end"
            className="fill-neutral-500"
            fontSize={10}
            style={{ fontVariantNumeric: "tabular-nums" }}
          >
            {fmt ? fmt(tv) : tv}
          </text>
        </g>
      ))}
      <text x={PAD_L} y={top - 9} fontSize={10} className="fill-neutral-400" style={{ letterSpacing: 0.4 }}>
        {title} <tspan className="fill-neutral-500">· {unit}</tspan>
      </text>
    </g>
  );

  return (
    <div ref={wrapRef} className="w-full">
      <svg
        ref={svgRef}
        width={width}
        height={totalH}
        viewBox={`0 0 ${width} ${totalH}`}
        role="img"
        aria-label="Drive timeline: speed, power and battery over time"
        style={{ display: "block", touchAction: "pan-y" }}
      >
        <defs>
          <linearGradient id="dt-speed-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" style={{ stopColor: C.speed, stopOpacity: 0.38 }} />
            <stop offset="1" style={{ stopColor: C.speed, stopOpacity: 0.03 }} />
          </linearGradient>
          <linearGradient id="dt-soc-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" style={{ stopColor: C.soc, stopOpacity: 0.22 }} />
            <stop offset="1" style={{ stopColor: C.soc, stopOpacity: 0.02 }} />
          </linearGradient>
          <linearGradient id="dt-draw-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" style={{ stopColor: C.draw, stopOpacity: 0.7 }} />
            <stop offset="1" style={{ stopColor: C.draw, stopOpacity: 0.06 }} />
          </linearGradient>
          <linearGradient id="dt-regen-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" style={{ stopColor: C.regen, stopOpacity: 0.06 }} />
            <stop offset="1" style={{ stopColor: C.regen, stopOpacity: 0.7 }} />
          </linearGradient>
          <clipPath id="dt-plot-clip">
            <rect x={PAD_L} y={0} width={plotW} height={totalH} />
          </clipPath>
        </defs>

        {/* Parked / gap dead zones: a quiet full-height band, no tint war. */}
        {parkBands.map((b, i) => {
          const x0 = sx(Math.max(b.x0, xMin));
          const x1 = sx(Math.min(b.x1, xMax));
          if (x1 - x0 < 2) return null;
          return (
            <rect
              key={`pk-${i}`}
              x={x0}
              y={speedTop}
              width={x1 - x0}
              height={plotBottom - speedTop}
              className="fill-neutral-500/10"
            >
              <title>{b.kind === "parked" ? "Parked" : "No telemetry"}</title>
            </rect>
          );
        })}

        {/* Speed */}
        <PanelFrame top={speedTop} h={speedH} title="Speed" unit="mph" tks={speedTicks} lo={0} hi={speedMax} />
        <g clipPath="url(#dt-plot-clip)">
          <path d={areaPath(speedPts, 0, speedMax, speedTop, speedH, 0)} fill="url(#dt-speed-fill)" />
          <path d={toPath(speedPts, 0, speedMax, speedTop, speedH)} fill="none" style={{ stroke: C.speed }} strokeWidth={1.8} strokeLinejoin="round" />
        </g>
        {/* moment flags */}
        {flags.map((m) => (
          <g key={`mf-${m.ms}-${m.label}`}>
            <line x1={sx(m.ms)} x2={sx(m.ms)} y1={speedTop + 16} y2={speedTop + speedH} stroke={momentColor(m.tone)} strokeWidth={1} strokeDasharray="2 3" opacity={0.7} />
            <path d={`M${sx(m.ms) - 4},${speedTop + 2} h8 l-4,8 z`} fill={momentColor(m.tone)}>
              <title>{`${m.label}: ${m.detail}`}</title>
            </path>
          </g>
        ))}

        {/* Power */}
        {powerPts.length > 1 ? (
          <g>
            <PanelFrame top={powerTop} h={powerH} title="Power" unit="kW" tks={powerTicks} lo={pLo} hi={pHi} fmt={(v) => (v > 0 ? `+${v}` : `${v}`)} />
            <g clipPath="url(#dt-plot-clip)">
              <path d={areaPath(powerPts, pLo, pHi, powerTop, powerH, 0, "pos")} fill="url(#dt-draw-fill)" />
              <path d={areaPath(powerPts, pLo, pHi, powerTop, powerH, 0, "neg")} fill="url(#dt-regen-fill)" />
              <path d={toPath(powerPts, pLo, pHi, powerTop, powerH)} fill="none" className="stroke-neutral-400" strokeWidth={1} />
            </g>
          </g>
        ) : null}

        {/* Battery + elevation */}
        <PanelFrame top={battTop} h={battH} title="Battery" unit="%" tks={socTicks} lo={sLo} hi={sHi} />
        <g clipPath="url(#dt-plot-clip)">
          {elevPts.length > 1 ? (
            <path d={areaPath(elevPts, eLo, eHi, battTop, battH, eLo)} style={{ fill: C.elev }} opacity={0.14} />
          ) : null}
          <path d={areaPath(socPts, sLo, sHi, battTop, battH, sLo)} fill="url(#dt-soc-fill)" />
          <path d={toPath(socPts, sLo, sHi, battTop, battH)} fill="none" style={{ stroke: C.soc }} strokeWidth={1.8} strokeLinejoin="round" />
        </g>
        {elevPts.length > 1 ? (
          <g className="fill-neutral-500" fontSize={10} style={{ fontVariantNumeric: "tabular-nums" }}>
            <text x={PAD_L + plotW} y={battTop - 8} textAnchor="end" opacity={0.8}>
              Elev <tspan>· ft</tspan>
            </text>
            {elevTicks.map((v) => (
              <text key={`et-${v}`} x={PAD_L + plotW + 5} y={Math.min(battTop + battH, Math.max(battTop + 8, yIn(v, eLo, eHi, battTop, battH) + 3.5))} opacity={0.8}>
                {v}
              </text>
            ))}
          </g>
        ) : null}

        {/* Pack temp (optional) */}
        {tempPts.length > 1 ? (
          <g>
            <PanelFrame top={tempTop} h={tempH} title="Pack temp" unit={tempUnit === "f" ? "°F" : "°C"} tks={[tLo, tHi]} lo={tLo} hi={tHi} />
            <g clipPath="url(#dt-plot-clip)">
              <path d={toPath(tempPts, tLo, tHi, tempTop, tempH)} fill="none" style={{ stroke: C.temp }} strokeWidth={1.6} />
            </g>
          </g>
        ) : null}

        {headwindPts.length > 1 ? (
          <g>
            <PanelFrame top={windTop} h={windH} title="Headwind" unit="mph" tks={[-wTop, 0, wTop]} lo={-wTop} hi={wTop} fmt={(v) => (v > 0 ? `+${v}` : `${v}`)} />
            <g clipPath="url(#dt-plot-clip)">
              <path d={toPath(headwindPts, -wTop, wTop, windTop, windH)} fill="none" className="stroke-neutral-400" strokeWidth={1.4} strokeDasharray="4 3" />
            </g>
          </g>
        ) : null}

        {/* Drive-mode band: only when the mode changes */}
        {modeSegments.length > 0 ? (
          <g clipPath="url(#dt-plot-clip)">
            {modeSegments.map((s, i) => {
              const x0 = sx(Math.max(s.x0, xMin));
              const x1 = sx(Math.min(s.x1, xMax));
              if (x1 <= x0) return null;
              return <rect key={`m-${i}`} x={x0} y={modeTop} width={x1 - x0} height={6} rx={2} fill={MODE_BAND[s.mode]} opacity={0.85} />;
            })}
          </g>
        ) : null}

        {/* Precipitation: only when there is some */}
        {precipBands.length > 0 ? (
          <g clipPath="url(#dt-plot-clip)">
            {precipBands.map((b, i) => (
              <rect key={`pc-${i}`} x={sx(b.x0)} y={precipTop} width={Math.max(1, sx(b.x1) - sx(b.x0))} height={8} rx={2} fill={b.color} opacity={0.8}>
                <title>{b.label}</title>
              </rect>
            ))}
          </g>
        ) : null}

        {/* Time axis: round clock times */}
        <g>
          {ticks.map((t, i) => {
            const x = sx(t);
            const anchor = i === 0 && ticks.length > 1 && x - PAD_L < 24 ? "start" : i === ticks.length - 1 && PAD_L + plotW - x < 24 ? "end" : "middle";
            return (
              <g key={`xt-${t}`}>
                <line x1={x} x2={x} y1={axisTop - 3} y2={axisTop} className="stroke-neutral-700" strokeWidth={1} />
                <text x={x} y={axisTop + 14} textAnchor={anchor} className="fill-neutral-500" fontSize={10} style={{ fontVariantNumeric: "tabular-nums" }}>
                  {clockLabel(t)}
                </text>
              </g>
            );
          })}
        </g>

        {/* Cursor */}
        {cx != null ? (
          <g pointerEvents="none">
            <line x1={cx} x2={cx} y1={speedTop} y2={plotBottom} className="stroke-neutral-300" strokeWidth={1} opacity={0.7} />
            {dot(speedPts, 0, speedMax, speedTop, speedH, C.speed)}
            {powerPts.length > 1 ? dot(powerPts, pLo, pHi, powerTop, powerH, C.draw) : null}
            {dot(socPts, sLo, sHi, battTop, battH, C.soc)}
          </g>
        ) : null}

        {/* Brush */}
        {brush ? (
          <rect
            x={Math.min(brush.x0, brush.x1)}
            y={speedTop}
            width={Math.abs(brush.x1 - brush.x0)}
            height={plotBottom - speedTop}
            className="fill-neutral-400/15 stroke-neutral-400"
            strokeWidth={0.8}
            pointerEvents="none"
          />
        ) : null}

        {/* Interaction layer */}
        <rect
          data-testid="timeline-scrub"
          x={PAD_L}
          y={speedTop}
          width={plotW}
          height={plotBottom - speedTop}
          fill="transparent"
          style={{ touchAction: "pan-y", cursor: "crosshair" }}
          onPointerDown={onDown}
          onPointerMove={onMove}
          onPointerUp={onUp}
          onPointerCancel={() => {
            // A vertical swipe the browser took over for scrolling.
            dragRef.current = null;
            setBrush(null);
            onCursorChange(null);
          }}
          onPointerLeave={(e) => {
            if (e.pointerType === "mouse" && !dragRef.current) onCursorChange(null);
          }}
          onDoubleClick={() => onViewWindowChange(null)}
        />
      </svg>
    </div>
  );
}

// ---- Readout row ----------------------------------------------------------

function Readout({
  cursorSample,
  cursorMs,
  power,
  soc,
  elev,
  wind,
  showTemp,
  tempUnit,
  drive,
  onDismiss,
}: {
  cursorSample: Sample | null;
  cursorMs: number | null;
  power: number | null;
  soc: number | null;
  elev: number | null;
  wind: number | null;
  showTemp: boolean;
  tempUnit: "f" | "c";
  drive: Drive;
  onDismiss?: () => void;
}) {
  const items: { color: string; label: string; value: string }[] = [];
  if (cursorSample && cursorMs != null) {
    items.push({ color: C.speed, label: "Speed", value: `${(cursorSample.SpeedMph || 0).toFixed(0)} mph` });
    if (power != null) {
      items.push({
        color: power >= 0 ? C.draw : C.regen,
        label: power >= 0 ? "Draw" : "Regen",
        value: `${power >= 0 ? "+" : "−"}${Math.abs(power).toFixed(0)} kW`,
      });
    }
    if (soc != null) items.push({ color: C.soc, label: "Battery", value: `${soc.toFixed(1)} %` });
    if (elev != null) items.push({ color: C.elev, label: "Elev", value: `${elev.toFixed(0)} ft` });
    // /api/samples coalesces a missing outside temp to 0, so 0 means "no reading".
    if (cursorSample.OutsideTempC != null && Number.isFinite(cursorSample.OutsideTempC) && cursorSample.OutsideTempC !== 0) {
      items.push({ color: C.elev, label: "Outside", value: fmtTemp(cursorSample.OutsideTempC, tempUnit) });
    }
    if (showTemp && cursorSample.pack_temp_avg_c) {
      items.push({ color: C.temp, label: "Pack", value: fmtTemp(cursorSample.pack_temp_avg_c, tempUnit) });
    }
    if (wind != null && Math.abs(wind) >= 1) {
      items.push({
        color: C.elev,
        label: "Wind",
        value: wind >= 0 ? `+${wind.toFixed(0)} mph head` : `${Math.abs(wind).toFixed(0)} mph tail`,
      });
    }
  }
  return (
    <div
      data-testid="timeline-readout"
      className="flex min-h-[34px] flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-1.5 text-[12px]"
    >
      {cursorSample && cursorMs != null ? (
        <>
          <span className="font-mono text-[11px] tabular-nums text-neutral-400">
            {clockLabel(new Date(cursorSample.At).getTime(), true)}
          </span>
          {items.map((it) => (
            <span key={it.label} className="inline-flex items-center gap-1.5">
              <span className="inline-block h-2 w-2 shrink-0 rounded-xs" style={{ background: it.color }} />
              <span className="text-neutral-500">{it.label}</span>
              <span className="font-medium tabular-nums text-neutral-100">{it.value}</span>
            </span>
          ))}
          {onDismiss ? (
            <button
              type="button"
              onClick={onDismiss}
              className="ml-auto rounded-full border border-neutral-800 px-2 py-0.5 text-[11px] text-neutral-500 hover:text-neutral-300"
              aria-label="Clear cursor"
            >
              ✕
            </button>
          ) : null}
        </>
      ) : (
        <>
          <span className="text-neutral-500">Whole drive</span>
          <span className="tabular-nums text-neutral-300">
            max <b className="font-medium text-neutral-100">{drive.MaxSpeedMph.toFixed(0)} mph</b>
          </span>
          <span className="tabular-nums text-neutral-300">
            avg <b className="font-medium text-neutral-100">{drive.AvgSpeedMph.toFixed(0)} mph</b>
          </span>
          {drive.EnergyUsedKWh > 0 ? (
            <span className="tabular-nums text-neutral-300">
              used <b className="font-medium text-neutral-100">{drive.EnergyUsedKWh.toFixed(1)} kWh</b>
            </span>
          ) : null}
          <span className="tabular-nums text-neutral-300">
            battery{" "}
            <b className="font-medium text-neutral-100">
              {drive.StartSoCPct.toFixed(0)}→{drive.EndSoCPct.toFixed(0)} %
            </b>
          </span>
        </>
      )}
    </div>
  );
}

// ---- Moments --------------------------------------------------------------

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
    <div className="flex min-w-0 flex-1 flex-nowrap items-center gap-1.5 overflow-x-auto pb-0.5 [scrollbar-width:none]">
      <span className="shrink-0 text-[10px] uppercase tracking-wide text-neutral-500">Moments</span>
      {moments.map((m) => {
        const active = cursorMs != null && Math.abs(cursorMs - m.ms) < 2000;
        return (
          <button
            key={`${m.ms}-${m.label}`}
            type="button"
            onClick={() => onCursorChange(active ? null : m.ms)}
            title={m.detail}
            className={`inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] ${
              active ? "border-neutral-500 bg-neutral-800 text-neutral-100" : "border-neutral-800 text-neutral-300 hover:bg-neutral-900"
            }`}
          >
            <span className="inline-block h-2 w-2 rounded-full" style={{ background: momentColor(m.tone) }} />
            <span className="font-mono tabular-nums text-neutral-500">{clockLabel(m.ms)}</span>
            {m.label}
          </button>
        );
      })}
    </div>
  );
}

function NoSamples() {
  return (
    <p className="text-sm text-neutral-500">
      No telemetry samples recorded for this drive, so there's no timeline to draw.
    </p>
  );
}
