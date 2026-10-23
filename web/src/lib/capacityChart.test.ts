import { describe, expect, it } from "vitest";
import { dots, specInRange, strength, trendRuns, yDomain, type CapacitySample } from "./capacityChart";

const s = (d: string, k: number, e?: number): CapacitySample => ({ at: `${d}T08:00:00Z`, kwh_effective: k, energy_kwh: e });

describe("capacity chart rules", () => {
  it("weights a dot by how much the charge added", () => {
    expect(strength(90, 138)).toBe(1);
    expect(strength(15, 138)).toBe(0.3);
    expect(strength(41.4, 138)).toBeCloseTo(0.5);
    expect(strength(undefined, 138)).toBe(0.6);
  });

  it("keeps the y range at the median ±15 % and pins outliers to its edge", () => {
    expect(yDomain(138.4)).toEqual([117, 160]);
    const d = dots([s("2026-01-03", 100), s("2026-01-04", 140)], 138.4);
    expect(d[0]).toMatchObject({ y: 117, outside: true });
    expect(d[1]).toMatchObject({ y: 140, outside: false });
    expect(specInRange(141, 138.4)).toBe(true);
    expect(specInRange(180, 138.4)).toBe(false);
  });

  it("draws the rolling median only where dense, never across a gap or a lone charge", () => {
    const jan = ["03", "06", "09", "13", "16"].map((d, i) => s(`2026-01-${d}`, 136 + i));
    const oct = ["01", "05", "09", "13"].map((d, i) => s(`2026-10-${d}`, 138 + i));
    const runs = trendRuns([...jan, s("2026-05-14", 127), ...oct]);
    expect(runs).toHaveLength(2);
    expect(runs[0]).toHaveLength(5);
    expect(runs[1]).toHaveLength(4);
    // Two charges a month apart: no run at all.
    expect(trendRuns([s("2026-01-01", 130), s("2026-02-15", 140)])).toEqual([]);
  });
});
