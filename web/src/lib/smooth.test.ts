import { describe, it, expect } from "vitest";
import { smoothGaussianTime, smoothSeries, type Pt } from "./smooth";

describe("smoothGaussianTime", () => {
  it("returns the input unchanged for fewer than 3 points", () => {
    const pts: Pt[] = [
      { x: 0, y: 1 },
      { x: 1000, y: 5 },
    ];
    expect(smoothGaussianTime(pts, 1000)).toBe(pts);
  });

  it("returns the input unchanged for a non-positive sigma", () => {
    const pts: Pt[] = [
      { x: 0, y: 1 },
      { x: 1000, y: 5 },
      { x: 2000, y: 9 },
    ];
    expect(smoothGaussianTime(pts, 0)).toBe(pts);
  });

  it("preserves x timestamps and point count", () => {
    const pts: Pt[] = [
      { x: 0, y: 0 },
      { x: 1000, y: 10 },
      { x: 2000, y: 0 },
      { x: 3000, y: 10 },
    ];
    const out = smoothGaussianTime(pts, 1000);
    expect(out).toHaveLength(pts.length);
    expect(out.map((p) => p.x)).toEqual([0, 1000, 2000, 3000]);
  });

  it("pulls an interior spike toward its neighbours", () => {
    const pts: Pt[] = [
      { x: 0, y: 0 },
      { x: 1000, y: 0 },
      { x: 2000, y: 100 },
      { x: 3000, y: 0 },
      { x: 4000, y: 0 },
    ];
    const out = smoothGaussianTime(pts, 1000);
    const peak = out[2].y;
    expect(peak).toBeGreaterThan(0);
    expect(peak).toBeLessThan(100);
  });

  it("leaves a flat series flat", () => {
    const pts: Pt[] = Array.from({ length: 8 }, (_, i) => ({ x: i * 1000, y: 42 }));
    const out = smoothGaussianTime(pts, 2000);
    for (const p of out) expect(p.y).toBeCloseTo(42, 6);
  });
});

describe("smoothSeries", () => {
  it("returns the input unchanged below the minimum window", () => {
    const pts = [
      { x: 0, y: 1 },
      { x: 1, y: 2 },
      { x: 2, y: 3 },
    ];
    expect(smoothSeries(pts, 1)).toBe(pts);
  });

  it("averages over the centered index window", () => {
    const pts = [
      { x: 0, y: 0 },
      { x: 1, y: 3 },
      { x: 2, y: 6 },
    ];
    // window 3 -> half 1; middle point averages all three: (0+3+6)/3 = 3
    const out = smoothSeries(pts, 3);
    expect(out[1].y).toBeCloseTo(3, 6);
  });
});
