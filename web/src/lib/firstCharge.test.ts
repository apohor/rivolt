import { describe, expect, it } from "vitest";
import type { Charge, Drive } from "./api";
import { firstChargeView } from "./firstCharge";

const NOW = new Date("2026-10-20T12:00:00Z");
const ch = (o: Partial<Charge>): Charge => ({ ID: "c1", StartedAt: "2026-10-19T02:00:00Z", EnergyAddedKWh: 40, MaxPowerKW: 11, Cost: 0, Currency: "USD", ...o }) as Charge;
const dr = (o: Partial<Drive>): Drive => ({ ID: "d1", StartedAt: "2026-10-19T14:00:00Z", DistanceMi: 20, EnergyUsedKWh: 8, ...o }) as Drive;

describe("firstChargeView", () => {
  it("A: a priced first charge, with a per-mile line from the range the charge added", () => {
    const v = firstChargeView({ charges: [ch({ Cost: 6, MilesAdded: 120 })], drives: [dr({ DistanceMi: 3000 })], homeRate: 0.15, now: NOW });
    expect(v).toMatchObject({ state: "A", kwh: 40, cost: 6, rate: 0.15, home: true, perMile: { miles: 120 } });
    expect((v as { perMile: { cost: number } }).perMile.cost).toBeCloseTo(0.05);
  });
  it("A: no per-mile line without a range figure, or when it would round to nothing", () => {
    expect(firstChargeView({ charges: [ch({ Cost: 6, MilesAdded: 0 })], drives: [], homeRate: 0.15, now: NOW })).not.toHaveProperty("perMile");
    expect(firstChargeView({ charges: [ch({ Cost: 0.5, MilesAdded: 400 })], drives: [], homeRate: 0.15, now: NOW })).not.toHaveProperty("perMile");
  });
  it("A also prices from the estimate, and notes a DC fast charge is not a home charge", () => {
    const v = firstChargeView({ charges: [ch({ estimated_cost: 5, MaxPowerKW: 150 })], drives: [], homeRate: 0.15, now: NOW });
    expect(v).toMatchObject({ state: "A", cost: 5, home: false });
    expect(v).not.toHaveProperty("perMile");
  });
  it("B: a first charge with no home rate", () => {
    expect(firstChargeView({ charges: [ch({})], drives: [], homeRate: 0, now: NOW })).toEqual({ lastDay: false, state: "B", chargeID: "c1", kwh: 40 });
  });
  it("C: a first drive before any charge", () => {
    expect(firstChargeView({ charges: [], drives: [dr({})], homeRate: 0.15, now: NOW })).toEqual({ lastDay: false, state: "C", driveID: "d1", miles: 20, miPerKwh: 2.5 });
  });
  it("takes the earliest charge, skips empty ones, and ends after a week or with nothing", () => {
    const v = firstChargeView({ charges: [ch({ ID: "late", StartedAt: "2026-10-19T20:00:00Z" }), ch({ ID: "early", StartedAt: "2026-10-18T20:00:00Z" }), ch({ ID: "zero", StartedAt: "2026-10-17T20:00:00Z", EnergyAddedKWh: 0 })], drives: [], homeRate: 0, now: NOW });
    expect(v).toMatchObject({ chargeID: "early" });
    expect(firstChargeView({ charges: [ch({ StartedAt: "2026-10-10T00:00:00Z" })], drives: [], homeRate: 0, now: NOW })).toBeNull();
    expect(firstChargeView({ charges: [], drives: [], homeRate: 0.15, now: NOW })).toBeNull();
  });
});

describe("the card's last day", () => {
  const DAY = 86_400_000;
  const c = (iso: string) => ({ ID: "c1", StartedAt: iso, EndedAt: iso, EnergyAddedKWh: 20, Cost: 5, Currency: "USD", MaxPowerKW: 7, MilesAdded: 0 }) as never;
  const first = "2026-10-20T10:00:00Z";
  const at = (days: number) => new Date(new Date(first).getTime() + days * DAY);
  it("is false for the first five days, true from the sixth full day until the card goes", () => {
    for (const d of [0, 1, 4, 5.9]) expect(firstChargeView({ charges: [c(first)], drives: [], homeRate: 0.15, now: at(d) })?.lastDay, `day ${d}`).toBe(false);
    for (const d of [6, 6.5, 7]) expect(firstChargeView({ charges: [c(first)], drives: [], homeRate: 0.15, now: at(d) })?.lastDay, `day ${d}`).toBe(true);
    expect(firstChargeView({ charges: [c(first)], drives: [], homeRate: 0.15, now: at(7.1) })).toBeNull();
  });
});
