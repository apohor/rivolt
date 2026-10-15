// Pure data model for the drive timeline: builders for the speed, mode,
// parked-band, precipitation, headwind and moment series, plus the warped
// time scale and round-time tick generator. No React, no DOM.

import type { DriveWeatherSamplePoint, Sample } from "./api";
import type { PowerPt } from "./power";

export type Mode = "Conserve" | "AllPurpose" | "Sport" | "Other";

export const SERIES = {
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

export const MODE_TINT: Record<Mode, string> = {
  Conserve: "#22c55e1f",
  AllPurpose: "#f973161f",
  Sport: "#ef44441f",
  Other: "transparent",
};

export const MODE_BAND: Record<Mode, string> = {
  Conserve: "#22c55e",
  AllPurpose: "#f97316",
  Sport: "#ef4444",
  Other: "#a3a3a3",
};

export const MODE_LABEL: Record<Mode, string> = {
  Conserve: "Conserve",
  AllPurpose: "All-Purpose",
  Sport: "Sport",
  Other: "Other",
};

export type ModeSegment = { x0: number; x1: number; mode: Mode };
export type ParkBand = { x0: number; x1: number; kind: "parked" | "gap" };

// A piecewise segment of the warped time axis: data-ms [m0,m1] maps to
// pixel [p0,p1]. Dead segments (parked / gap) get a fixed narrow width.
export type TimeSeg = { m0: number; m1: number; p0: number; p1: number; dead: boolean };

// buildTimeScale warps the linear time axis so parked stretches and
// telemetry gaps take a fixed narrow width (bandPx) instead of their real
// duration, and the active driving time fills the rest proportionally.
// Returns sx (ms→px) and its inverse; both are continuous and monotonic so
// the cursor, brush and every data layer stay consistent. With no dead
// zones it degrades to the original linear mapping.
export function buildTimeScale(
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
export type PrecipBand = {
  x0: number;
  x1: number;
  color: string;
  label: string;
};
export type Moment = {
  ms: number;
  label: string;
  detail: string;
  tone: "info" | "warning" | "success" | "neutral";
};


export function buildSpeedPts(samples: Sample[]): { x: number; y: number }[] {
  const raw = samples.map((p) => ({
    x: new Date(p.At).getTime(),
    y: p.SpeedMph || 0,
  }));
  if (raw.length === 0) return raw;
  const last = raw[raw.length - 1];
  if ((last.y ?? 0) <= 0.5) return raw;
  return [...raw, { x: last.x + 1000, y: 0 }];
}

export function buildModeSegments(
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
export function buildParkBands(
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

export function classifyMode(s: Sample): Mode {
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

export function buildPrecipBands(
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

export function precipColor(cond: string | undefined): string | null {
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

export function buildHeadwindPts(
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

export function detectMoments(
  speedPts: { x: number; y: number }[],
  socPts: { x: number; y: number }[],
  powerPts: PowerPt[],
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
    // The final stop of the drive isn't an event: speed reaching ~0 in
    // the last ~30 s is the arrival, not a hard brake.
    const endMs = speedPts[speedPts.length - 1].x;
    if (speedPts[i].x > endMs - 30_000 && speedPts[i].y < 5) continue;
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


export function fmtTemp(c: number, unit: "f" | "c"): string {
  if (unit === "f") return `${(c * 1.8 + 32).toFixed(0)}°F`;
  return `${c.toFixed(0)}°C`;
}

export function ySolve(
  v: number,
  yMin: number,
  yMax: number,
  top: number,
  height: number,
): number {
  if (yMax === yMin) return top + height / 2;
  return top + height - ((v - yMin) / (yMax - yMin)) * height;
}

export function nearestY(
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

export function snapToSample(
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

// Candidate tick steps in minutes: round clock boundaries only.
const TICK_STEPS_MIN = [1, 2, 5, 10, 15, 30, 60, 120, 180, 360, 720, 1440];

// localMinuteOfDay is the minutes since local midnight of `ms` in `tz`
// (the browser zone when undefined).
function localMinuteOfDay(ms: number, tz?: string): number {
  const parts = new Intl.DateTimeFormat("en-US", {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone: tz,
  }).formatToParts(new Date(ms));
  const h = Number(parts.find((p) => p.type === "hour")?.value ?? 0);
  const m = Number(parts.find((p) => p.type === "minute")?.value ?? 0);
  return h * 60 + m;
}

// niceTimeTicks returns up to maxTicks ticks on round clock times (5,
// 10, 15, 30, 60 min ... in the display zone) inside [min, max]. It
// always returns at least the two ends when the span is too short for a
// round tick, so an axis is never unlabeled.
export function niceTimeTicks(min: number, max: number, maxTicks: number, tz?: string): number[] {
  const span = max - min;
  if (!(span > 0)) return [min];
  const stepMin =
    TICK_STEPS_MIN.find((s) => span / (s * 60_000) <= Math.max(1, maxTicks - 1)) ??
    TICK_STEPS_MIN[TICK_STEPS_MIN.length - 1];
  const stepMs = stepMin * 60_000;
  // First round boundary at or after min: walk minute by minute (at most
  // one step) until the local minute-of-day is a multiple of the step.
  let t = Math.ceil(min / 60_000) * 60_000;
  const limit = t + stepMs;
  while (t < limit && localMinuteOfDay(t, tz) % stepMin !== 0) t += 60_000;
  const out: number[] = [];
  for (; t <= max; t += stepMs) out.push(t);
  if (out.length >= 2) return out;
  return [min, max];
}

export function momentColor(tone: Moment["tone"]): string {
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

export function chipToneClasses(tone: Moment["tone"], active: boolean): string {
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

export function interpHex(a: string, b: string, t: number): string {
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

export function pad2(n: number): string {
  return n.toString(16).padStart(2, "0");
}

// ---- Path interpolation --------------------------------------------------

export function linePath(pts: { x: number; y: number }[]): string {
  if (pts.length === 0) return "";
  let d = `M ${pts[0].x.toFixed(2)},${pts[0].y.toFixed(2)}`;
  for (let i = 1; i < pts.length; i++) {
    d += ` L ${pts[i].x.toFixed(2)},${pts[i].y.toFixed(2)}`;
  }
  return d;
}

// Fritsch–Carlson monotone cubic — preserves local extrema (no overshoot).
export function monotonePath(pts: { x: number; y: number }[]): string {
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
