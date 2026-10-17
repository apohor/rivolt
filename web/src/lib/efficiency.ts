// Efficiency insights helpers. The server sends labels and values in the requested units, so
// there is no conversion here: only the bar scale and "is there anything to chart".
import type { EfficiencyBucket } from "./api";

// The bar scale: a little above the best bucket (or the overall), never below 1.
export function scaleMax(values: (number | null)[], overall: number | null | undefined): number {
  const m = Math.max(1, overall ?? 0, ...values.map((v) => v ?? 0));
  return m * 1.15;
}

// Whether a set of buckets has at least one value worth drawing (a figure on enough distance).
export const hasValues = (bs: EfficiencyBucket[] | undefined): boolean => !!bs && bs.some((b) => b.efficiency != null && !b.not_enough_data);

// Whether a bucket has a figure at all, even if it is thin.
export const hasFigure = (b: EfficiencyBucket): boolean => b.efficiency != null;
