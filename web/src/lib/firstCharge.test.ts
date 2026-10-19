import { describe, expect, it } from "vitest";
import type { Charge, Drive } from "./api";
import { firstChargeView } from "./firstCharge";

const NOW = new Date("2026-10-20T12:00:00Z");
const ch = (o: Partial<Charge>): Charge => ({ ID: "c1", StartedAt: "2026-10-19T02:00:00Z", EnergyAddedKWh: 40, MaxPowerKW: 11, Cost: 0, Currency: "USD", ...o }) as Charge;
const dr = (o: Partial<Drive>): Drive => ({ ID: "d1", StartedAt: "2026-10-19T14:00:00Z", DistanceMi: 20, EnergyUsedKWh: 8, ...o }) as Drive;

describe("firstChargeView", () => {
  it("A: a priced first charge, with a per-mile line when there are drives", () => {
    const v = firstChargeView({ charges: [ch({ Cost: 6 })], drives: [dr({ DistanceMi: 30 })], homeRate: 0.15, now: NOW });
    expect(v).toMatchObject({ state: "A", kwh: 40, cost: 6, rate: 0.15, home: true, perMile: { miles: 30 } });
    expect((v as { perMile: { cost: number } }).perMile.cost).toBeCloseTo(0.2);
  });
  it("A: the per-mile line counts only the first week, not a long history", () => {
    const v = firstChargeView({
      charges: [ch({ Cost: 6, StartedAt: "2026-10-19T02:00:00Z" })],
      drives: [dr({ DistanceMi: 30 }), dr({ ID: "later", StartedAt: "2026-10-28T10:00:00Z", DistanceMi: 3000 })],
      homeRate: 0.15,
      now: NOW,
    });
    expect(v).toMatchObject({ perMile: { miles: 30 } });
    // A cost per mile that rounds to nothing is left out rather than shown as $0.00/mi.
    const w = firstChargeView({ charges: [ch({ Cost: 0.5 })], drives: [dr({ DistanceMi: 400 })], homeRate: 0.15, now: NOW });
    expect(w).not.toHaveProperty("perMile");
  });
  it("A also prices from the estimate, and notes a DC fast charge is not a home charge", () => {
    const v = firstChargeView({ charges: [ch({ estimated_cost: 5, MaxPowerKW: 150 })], drives: [], homeRate: 0.15, now: NOW });
    expect(v).toMatchObject({ state: "A", cost: 5, home: false });
    expect(v).not.toHaveProperty("perMile");
  });
  it("B: a first charge with no home rate", () => {
    expect(firstChargeView({ charges: [ch({})], drives: [], homeRate: 0, now: NOW })).toEqual({ state: "B", chargeID: "c1", kwh: 40 });
  });
  it("C: a first drive before any charge", () => {
    expect(firstChargeView({ charges: [], drives: [dr({})], homeRate: 0.15, now: NOW })).toEqual({ state: "C", driveID: "d1", miles: 20, miPerKwh: 2.5 });
  });
  it("takes the earliest charge, skips empty ones, and ends after a week or with nothing", () => {
    const v = firstChargeView({ charges: [ch({ ID: "late", StartedAt: "2026-10-19T20:00:00Z" }), ch({ ID: "early", StartedAt: "2026-10-18T20:00:00Z" }), ch({ ID: "zero", StartedAt: "2026-10-17T20:00:00Z", EnergyAddedKWh: 0 })], drives: [], homeRate: 0, now: NOW });
    expect(v).toMatchObject({ chargeID: "early" });
    expect(firstChargeView({ charges: [ch({ StartedAt: "2026-10-10T00:00:00Z" })], drives: [], homeRate: 0, now: NOW })).toBeNull();
    expect(firstChargeView({ charges: [], drives: [], homeRate: 0.15, now: NOW })).toBeNull();
  });
});
