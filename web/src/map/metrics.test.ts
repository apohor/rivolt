import { describe, expect, it } from "vitest";
import { cumulativeMiles, efficiencySeries, elevationSeries, gradientStops, percentileRange, rampColor, RAMPS, seriesFor, smooth, type Sample } from "./metrics";

// A made-up straight drive near (10, 10): n samples about 0.1 mi apart, speed varying, battery falling, altitude rising.
function drive(n: number, over: Partial<Sample> = {}, f: (i: number) => Partial<Sample> = () => ({})): Sample[] {
  return Array.from({ length: n }, (_, i) => ({
    lat: 10 + i * 0.0015, // ~0.1 mi per step
    lon: 10,
    t: 1_700_000_000 + i * 20,
    s: 40 + (i % 5) * 5,
    alt: 100 + i * 2,
    soc: 80 - i * 0.12,
    odo: 5000 + i * 0.1,
    ...over,
    ...f(i),
  }));
}

describe("percentileRange", () => {
  it("ignores one spike (5th to 95th, not min to max)", () => {
    const v = Array.from({ length: 100 }, (_, i) => 50 + (i % 10));
    v[7] = 400;
    const [lo, hi] = percentileRange(v);
    expect(hi).toBeLessThan(70);
    expect(lo).toBeGreaterThanOrEqual(50);
  });
  it("never returns an empty range", () => {
    const [lo, hi] = percentileRange([5, 5, 5]);
    expect(hi).toBeGreaterThan(lo);
    expect(percentileRange([])).toEqual([0, 1]);
  });
});

describe("smooth", () => {
  it("averages within the distance window and skips gaps", () => {
    const cum = [0, 1, 2, 3, 4];
    const out = smooth([10, NaN, 30, 40, 50], cum, 1);
    expect(out[1]).toBe(20); // mean of 10 and 30
    expect(out[4]).toBe(45);
  });
});

describe("modes", () => {
  it("a long drive with battery and altitude has all three; speed first", () => {
    const { modes } = seriesFor(drive(120), 135);
    expect(modes).toEqual(["speed", "eff", "elev"]);
  });
  it("efficiency needs 5 miles and a 5-point battery change", () => {
    const cum = (s: Sample[]) => cumulativeMiles(s);
    const short = drive(30); // 3 mi
    expect(efficiencySeries(short, 135, cum(short))).toBeNull();
    const flat = drive(120, {}, () => ({ soc: 60 })); // 12 mi, battery did not change
    expect(efficiencySeries(flat, 135, cum(flat))).toBeNull();
    const ok = drive(120);
    expect(efficiencySeries(ok, 135, cum(ok))).not.toBeNull();
  });
  it("efficiency is miles per kWh from the battery: 12 mi on 14.4 percent of 135 kWh", () => {
    const s = drive(121);
    const eff = efficiencySeries(s, 135, cumulativeMiles(s))!;
    const mid = eff[60];
    // 0.1 mi per 0.12 percent => 0.162 kWh => 0.617 mi... per sample step; ratio = 0.1 / (0.0012 * 135) = 0.617 mi/kWh
    expect(mid).toBeGreaterThan(0.55);
    expect(mid).toBeLessThan(0.7);
  });
  it("efficiency is withheld without a pack size or battery values", () => {
    const s = drive(120);
    expect(efficiencySeries(s, undefined, cumulativeMiles(s))).toBeNull();
    const noSoc = drive(120, { soc: undefined });
    expect(efficiencySeries(noSoc, 135, cumulativeMiles(noSoc))).toBeNull();
  });
  it("elevation needs altitude on 80 percent of the samples", () => {
    const s = drive(100, {}, (i) => (i % 5 === 0 ? { alt: undefined } : {})); // 80 percent: allowed
    expect(elevationSeries(s, cumulativeMiles(s))).not.toBeNull();
    const less = drive(100, {}, (i) => (i % 4 === 0 ? { alt: undefined } : { alt: undefined, ...(i % 2 ? { alt: 5 } : {}) }));
    expect(elevationSeries(less, cumulativeMiles(less))).toBeNull();
  });
  it("a drive with only speed offers one mode", () => {
    const { modes } = seriesFor(drive(120, { alt: undefined, soc: undefined }), 135);
    expect(modes).toEqual(["speed"]);
  });
});

describe("gradientStops", () => {
  it("is strictly increasing from 0 to 1 and clamps values into the ramp", () => {
    const cum = [0, 1, 2, 3, 4];
    const vals = [10, 20, 30, 40, 1000]; // the last is far above hi
    const stops = gradientStops(cum, vals, 10, 40, RAMPS.dark.speed, 20);
    const progress = stops.filter((_, i) => i % 2 === 0) as number[];
    for (let i = 1; i < progress.length; i++) expect(progress[i]).toBeGreaterThan(progress[i - 1]);
    expect(progress[0]).toBe(0);
    expect(stops[1]).toBe(rampColor(RAMPS.dark.speed, 0));
    expect(stops[stops.length - 1]).toBe(rampColor(RAMPS.dark.speed, 0.9999));
  });
  it("a zero-length track still gives a valid two-stop gradient", () => {
    const stops = gradientStops([0, 0], [5, 5], 0, 10, RAMPS.light.eff);
    expect(stops).toHaveLength(4);
  });
  it("a highway drive reads teal to yellow, not red", () => {
    // Speeds all in the 60s: scaled to their own percentiles, the ramp spans fully, so the middle is teal.
    const cum = Array.from({ length: 50 }, (_, i) => i * 0.2);
    const vals = cum.map((_, i) => 60 + (i % 10));
    const [lo, hi] = percentileRange(vals);
    const stops = gradientStops(cum, vals, lo, hi, RAMPS.dark.speed);
    const colours = stops.filter((_, i) => i % 2 === 1) as string[];
    expect(colours.every((c) => !/^#ff[0-6]/i.test(c))).toBe(true);
  });
});
