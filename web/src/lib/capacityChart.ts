// The "Estimated usable capacity" chart's rules, shared in spirit with the iPhone (CapacityMath.swift): one dot per
// charge estimate (no line between them, which would invent data across empty months), weighted by how much the charge
// added, a rolling median only where estimates are dense, and a calm y range so a few kWh of noise don't read as
// degradation. Every figure is an estimate, never a measurement.

export type CapacitySample = { at: string; kwh_effective: number; energy_kwh?: number | null; soc_delta_pct?: number | null };
export type CapacityDot = { x: number; y: number; strength: number; energyKwh: number | null; outside: boolean };
export type TrendPoint = { x: number; y: number };

const DAY = 86_400_000;
// Rolling median: estimates within ±15 days, drawn only with at least 3 of them; broken across gaps over 30 days.
export const TREND_WINDOW_DAYS = 15;
export const TREND_MIN_POINTS = 3;
export const TREND_GAP_DAYS = 30;
// The y range: the median ±15 %.
export const Y_SPAN = 0.15;

// How much a dot is trusted, 0.3 to 1: a charge adding 60 % of the pack or more is full strength; without the energy,
// a middling 0.6.
export function strength(energyKwh: number | null | undefined, medianKwh: number): number {
  if (energyKwh == null || !Number.isFinite(energyKwh) || medianKwh <= 0) return 0.6;
  return Math.min(1, Math.max(0.3, energyKwh / (0.6 * medianKwh)));
}

export const dotRadius = (s: number) => 2.5 + 3 * s;
export const dotOpacity = (s: number) => 0.25 + 0.75 * s;

export function yDomain(medianKwh: number): [number, number] {
  return [Math.floor(medianKwh * (1 - Y_SPAN)), Math.ceil(medianKwh * (1 + Y_SPAN))];
}

export function dots(samples: CapacitySample[], medianKwh: number): CapacityDot[] {
  const [lo, hi] = yDomain(medianKwh);
  return samples
    .map((s) => {
      const y = s.kwh_effective;
      return {
        x: new Date(s.at).getTime(),
        y: Math.min(hi, Math.max(lo, y)),
        strength: strength(s.energy_kwh, medianKwh),
        energyKwh: s.energy_kwh ?? null,
        outside: y < lo || y > hi,
      };
    })
    .filter((d) => Number.isFinite(d.x) && Number.isFinite(d.y))
    .sort((a, b) => a.x - b.x);
}

function median(v: number[]): number {
  const s = [...v].sort((a, b) => a - b);
  const m = Math.floor(s.length / 2);
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

// The rolling median as separate runs: a point only where at least TREND_MIN_POINTS estimates lie within
// ±TREND_WINDOW_DAYS, and a new run after any gap longer than TREND_GAP_DAYS (or a sparse point).
export function trendRuns(samples: CapacitySample[]): TrendPoint[][] {
  const pts = samples
    .map((s) => ({ x: new Date(s.at).getTime(), y: s.kwh_effective }))
    .filter((p) => Number.isFinite(p.x) && Number.isFinite(p.y))
    .sort((a, b) => a.x - b.x);
  const runs: TrendPoint[][] = [];
  let run: TrendPoint[] = [];
  for (let i = 0; i < pts.length; i++) {
    const near = pts.filter((q) => Math.abs(q.x - pts[i].x) <= TREND_WINDOW_DAYS * DAY).map((q) => q.y);
    const gap = i > 0 && pts[i].x - pts[i - 1].x > TREND_GAP_DAYS * DAY;
    if (gap || near.length < TREND_MIN_POINTS) {
      if (run.length >= 2) runs.push(run);
      run = [];
    }
    if (near.length >= TREND_MIN_POINTS) run.push({ x: pts[i].x, y: median(near) });
  }
  if (run.length >= 2) runs.push(run);
  return runs;
}

// The rated pack is drawn only when it falls inside the calm range (a reference, not a verdict).
export function specInRange(specKwh: number, medianKwh: number): boolean {
  const [lo, hi] = yDomain(medianKwh);
  return specKwh > 0 && specKwh >= lo && specKwh <= hi;
}
