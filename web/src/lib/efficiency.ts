// Efficiency breakdown helpers: bucket labels in the user's units, the display scale, and the
// "is there anything to chart" test. Values arrive in mi/kWh, °C and mph.
import type { EfficiencyBucket } from "./api";
import type { DistanceUnit, TemperatureUnit } from "./preferences";

const KM_PER_MI = 1.609344;

export const effUnit = (u: DistanceUnit) => (u === "km" ? "km/kWh" : "mi/kWh");
export const effValue = (miPerKwh: number, u: DistanceUnit) => (u === "km" ? miPerKwh * KM_PER_MI : miPerKwh);

const c2f = (c: number) => (c * 9) / 5 + 32;
const t = (c: number, u: TemperatureUnit) => Math.round(u === "f" ? c2f(c) : c);
const sp = (mph: number, u: DistanceUnit) => Math.round(u === "km" ? mph * KM_PER_MI : mph);

// A short axis label for a bucket: "<0°", "0–10°", "30°+" / "<55", "55–65", "75+" / the mode name.
export function bucketLabel(kind: "temp" | "speed" | "mode", b: EfficiencyBucket, dist: DistanceUnit, temp: TemperatureUnit): string {
  if (kind === "mode") return b.label || b.key;
  if (kind === "temp") {
    const lo = b.lo_c, hi = b.hi_c;
    if (lo == null && hi != null) return `<${t(hi, temp)}°`;
    if (hi == null && lo != null) return `${t(lo, temp)}°+`;
    if (lo != null && hi != null) return `${t(lo, temp)}–${t(hi, temp)}°`;
    return b.label || b.key;
  }
  const lo = b.lo_mph, hi = b.hi_mph;
  if (lo == null && hi != null) return `<${sp(hi, dist)}`;
  if (hi == null && lo != null) return `${sp(lo, dist)}+`;
  if (lo != null && hi != null) return `${sp(lo, dist)}–${sp(hi, dist)}`;
  return b.label || b.key;
}

// The bar scale: a little above the best bucket (or the overall), never below 1.
export function scaleMax(values: (number | null)[], overall: number | null | undefined): number {
  const m = Math.max(1, overall ?? 0, ...values.map((v) => v ?? 0));
  return m * 1.15;
}

// Whether a set of buckets has at least one value to draw.
export const hasValues = (bs: EfficiencyBucket[] | undefined): boolean => !!bs && bs.some((b) => b.mi_per_kwh != null);
