import { describe, it, expect } from "vitest";
import { analyzeDrivePower, buildElevPts, derivePower } from "./power";
import type { Drive, Sample } from "./api";

// Mirrors internal/recap/power_test.go. The two implementations are
// documented as needing to stay in lock-step, so the regressions are
// asserted on both sides.

const BASE = Date.UTC(2026, 7, 1, 12, 0, 0);

// Build samples from (offsetSeconds, speedMph) pairs. Offsets are
// fractional so tests can express sub-second spacing, which is the
// point of two of them.
function mkSamples(pts: [number, number][]): Sample[] {
  return pts.map(
    ([off, mph]) =>
      ({
        At: new Date(BASE + off * 1000).toISOString(),
        SpeedMph: mph,
      }) as unknown as Sample,
  );
}

const drive = (kwh: number) => ({ EnergyUsedKWh: kwh }) as unknown as Drive;

describe("analyzeDrivePower", () => {
  it("reports physically plausible totals on clean samples", () => {
    const pts: [number, number][] = [];
    for (let i = 0; i < 36; i++) pts.push([i * 5, 55]);
    for (let i = 1; i <= 4; i++) pts.push([180 + i * 5, 55 - i * 13.75]);

    const pa = analyzeDrivePower(mkSamples(pts), drive(1.2));

    expect(pa.drawKwh).toBeGreaterThan(0);
    // The invariant that actually matters: you cannot recover more
    // than you spent.
    expect(pa.regenKwh).toBeLessThanOrEqual(pa.drawKwh);
    expect(pa.regenPct).toBeGreaterThanOrEqual(0);
    expect(pa.regenPct).toBeLessThanOrEqual(100);
  });

  // Regression: near-duplicate rows must not blow up dv/dt.
  //
  // The live-merge path writes near-duplicate rows when a WS frame
  // and a REST fallback land together, with speeds differing by up to
  // 63 mph. 5,423 recorded intervals sit between 1 ms and 0.5 s.
  // fAccel = mass × dv/dt turned those into ~1e6 kW spikes which,
  // emitted at interval midpoints, were then integrated across the
  // seconds separating them from their neighbours — landing in
  // regenKwh. On real drives this reported 229 kWh of regen on a
  // 3.89 kWh drive. The calibration clamp of [0.5, 2.0] cannot
  // absorb that.
  it("rejects near-duplicate samples instead of integrating them", () => {
    const pts: [number, number][] = [];
    for (let i = 0; i < 20; i++) pts.push([i * 5, 55]);
    const clean = analyzeDrivePower(mkSamples(pts), drive(0.8));

    // 1 ms after the 50 s row. Sample.At is an ISO string and Date
    // is millisecond-resolution — and the Go twin works in UnixMilli
    // — so anything finer truncates to dt = 0 and is rejected as
    // non-monotonic, testing nothing. 1 ms is the tightest interval
    // either model can see, and it is plenty: a 55 mph drop over
    // 1 ms is ~24,600 m/s².
    //
    // Inserted AFTER the 50 s sample so the series stays monotonic.
    const poisoned: [number, number][] = [
      ...pts.slice(0, 11),
      [50.001, 0],
      ...pts.slice(11),
    ];
    const got = analyzeDrivePower(mkSamples(poisoned), drive(0.8));

    expect(got.regenKwh).toBeLessThanOrEqual(got.drawKwh);
    expect(got.regenKwh).toBeLessThan(clean.regenKwh + 0.5);
  });

  // Regression: a telemetry hole must not be integrated across.
  //
  // derivePower emits no point for a rejected interval, but the
  // integrators used to compute dt between surviving points — spanning
  // the hole. 968 of 1860 recorded drives contain >30 s gaps; one is
  // 95.9 % gap by time, so a boundary sample sitting in regen got
  // multiplied across minutes.
  it("does not integrate across telemetry gaps", () => {
    const contiguous: [number, number][] = [];
    for (let i = 0; i < 12; i++) contiguous.push([i * 5, 45]);

    // Identical modelled power either side; any extra energy the
    // gapped version reports is phantom.
    const gapped: [number, number][] = [...contiguous.slice(0, 6)];
    for (let i = 6; i < 12; i++) gapped.push([1200 + i * 5, 45]);

    const a = analyzeDrivePower(mkSamples(contiguous), drive(0.4));
    const b = analyzeDrivePower(mkSamples(gapped), drive(0.4));

    expect(b.regenKwh).toBeLessThan(a.regenKwh + 0.05);
    expect(b.drawKwh).toBeLessThan(a.drawKwh * 1.5);
  });
});

describe("derivePower", () => {
  it("marks discontinuities so integrators can skip them", () => {
    const ss = mkSamples([
      [0, 40],
      [5, 42],
      [10, 41],
      [600, 44], // after a 10-minute hole
      [605, 43],
      [610, 45],
    ]);
    const out = derivePower(ss, buildElevPts(ss), 0);

    expect(out.length).toBeGreaterThanOrEqual(4);
    // The first point has no predecessor to integrate against.
    expect(out[0].gap).toBe(true);
    expect(out.slice(1).filter((p) => p.gap).length).toBe(1);
  });

  it("drops both sub-second and over-long intervals", () => {
    const ss = mkSamples([
      [0, 30],
      [0.0001, 60], // far below MIN_DT_SEC
      [5, 30],
      [10, 31],
      [500, 33], // far above MAX_DT_SEC
      [505, 32],
    ]);
    for (const p of derivePower(ss, buildElevPts(ss), 0)) {
      expect(Math.abs(p.y)).toBeLessThan(1e4);
    }
  });
});
