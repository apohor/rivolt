import { describe, expect, it } from "vitest";
import type { EfficiencyBucket } from "./api";
import { hasFigure, hasValues, scaleMax } from "./efficiency";

const bk = (o: Partial<EfficiencyBucket> = {}): EfficiencyBucket => ({ key: "k", label: "L", distance: 100, energy_kwh: 40, efficiency: 2.5, drives: 9, not_enough_data: false, ...o });

describe("scaleMax", () => {
  it("scales a little above the best value, never below 1", () => {
    expect(scaleMax([2, null, 3], 2.3)).toBeCloseTo(3.45, 2);
    expect(scaleMax([null], null)).toBeCloseTo(1.15, 2);
  });
});

describe("hasValues / hasFigure", () => {
  it("needs a bucket with a figure on enough distance", () => {
    expect(hasValues(undefined)).toBe(false);
    expect(hasValues([bk({ efficiency: null, drives: 0 })])).toBe(false);
    expect(hasValues([bk({ not_enough_data: true })])).toBe(false);
    expect(hasValues([bk({ not_enough_data: true }), bk()])).toBe(true);
  });
  it("a thin bucket still has a figure; an empty one does not", () => {
    expect(hasFigure(bk({ not_enough_data: true }))).toBe(true);
    expect(hasFigure(bk({ efficiency: null }))).toBe(false);
  });
});
