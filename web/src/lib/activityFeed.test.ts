import { describe, expect, it } from "vitest";
import type { Charge, Drive } from "./api";
import { buildFeed, chargeCostLine, driveMissingEnergy } from "./activityFeed";

const HOME = { latitude: 40, longitude: -90 };
function drive(id: string, start: string, mi: number, p: Partial<Drive> = {}): Drive {
  return {
    ID: id, StartedAt: start, EndedAt: start, DistanceMi: mi, EnergyUsedKWh: mi * 0.4,
    StartLat: 40, StartLon: -90, EndLat: 41, EndLon: -90, ...p,
  } as Drive;
}
function charge(id: string, start: string, p: Partial<Charge> = {}): Charge {
  return { ID: id, StartedAt: start, EndedAt: start, FinalState: "charging_complete", Cost: 0, Currency: "USD", MaxPowerKW: 7, Lat: 40, Lon: -90, ...p } as Charge;
}
const opts = { home: HOME, filter: "all" as const, tz: "UTC" };

describe("buildFeed", () => {
  it("groups by day, newest first, and merges drives with charges", () => {
    const days = buildFeed(
      [drive("d1", "2026-10-02T10:00:00Z", 10), drive("d0", "2026-10-01T10:00:00Z", 10)],
      [charge("c1", "2026-10-02T20:00:00Z")],
      opts,
    );
    expect(days.map((d) => d.day)).toEqual(["2026-10-02", "2026-10-01"]);
    expect(days[0].items.map((i) => i.kind)).toEqual(["charge", "drive"]);
  });
  it("folds runs of short drives into one errands row, not lone ones", () => {
    const days = buildFeed(
      [
        drive("a", "2026-10-02T08:00:00Z", 1.2, { estimated_cost: 0.1 }),
        drive("b", "2026-10-02T09:00:00Z", 0.8, { estimated_cost: 0.1 }),
        drive("c", "2026-10-02T12:00:00Z", 20),
        drive("d", "2026-10-02T15:00:00Z", 1),
      ],
      [],
      opts,
    );
    const kinds = days[0].items.map((i) => i.kind);
    expect(kinds).toEqual(["drive", "drive", "errands"]);
    const e = days[0].items[2];
    expect(e.kind === "errands" && e.drives.length).toBe(2);
    expect(e.kind === "errands" && e.cost).toBeCloseTo(0.2);
  });
  it("never sums a partial errand cost", () => {
    const days = buildFeed(
      [drive("a", "2026-10-02T08:00:00Z", 1, { estimated_cost: 0.1 }), drive("b", "2026-10-02T09:00:00Z", 1)],
      [],
      opts,
    );
    const e = days[0].items[0];
    expect(e.kind === "errands" && e.cost).toBeNull();
  });
  it("filters, and labels Home only when inside the radius", () => {
    const only = buildFeed([drive("a", "2026-10-02T08:00:00Z", 9)], [charge("c", "2026-10-02T09:00:00Z")], { ...opts, filter: "charges" });
    expect(only[0].items).toHaveLength(1);
    expect(only[0].items[0].kind).toBe("charge");
    const d = buildFeed([drive("a", "2026-10-02T08:00:00Z", 9)], [], opts)[0].items[0];
    expect(d.kind === "drive" && d.from).toBe("Home");
    expect(d.kind === "drive" && d.to).toBeNull();
  });
});

describe("data-quality lines", () => {
  it("prices, frees, estimates and flags unpriced", () => {
    expect(chargeCostLine(charge("x", "2026-10-02T00:00:00Z", { Cost: 4.5 })).tone).toBe("ok");
    expect(chargeCostLine(charge("x", "2026-10-02T00:00:00Z", { CostSource: "user" })).text).toBe("Free");
    expect(chargeCostLine(charge("x", "2026-10-02T00:00:00Z", { estimated_cost: 3 })).tone).toBe("est");
    expect(chargeCostLine(charge("x", "2026-10-02T00:00:00Z")).text).toBe("Unpriced");
    expect(driveMissingEnergy(drive("a", "2026-10-02T08:00:00Z", 8, { EnergyUsedKWh: 0 }))).toBe(true);
  });
});
