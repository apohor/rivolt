import { describe, expect, it } from "vitest";
import type { PlanVsActual, PvaActual, PvaPlanned } from "./api";
import { rows, stateOf } from "./planVsActual";

const planned: PvaPlanned = { currency: "USD", cost: 50, kwh: 150, stops: 3, network: null, arrival_soc: 20, duration_min: 600 };
const actual = (o: Partial<PvaActual> = {}): PvaActual => ({
  cost: 46, cost_source: "network_estimate", kwh: 140, stops: 4, networks: [], arrival_soc: 15, duration_min: 660,
  unpriced_sessions: 0, started_at: "2026-10-10T00:00:00Z", ...o,
});
const pva = (o: Partial<PlanVsActual>): PlanVsActual => ({ saved_trip_id: "s", status: "matched", planned, actual: actual(), ...o });

describe("stateOf", () => {
  it("covers each state", () => {
    expect(stateOf(pva({ status: "none" }))).toBe("none");
    expect(stateOf(pva({ status: "suggested" }))).toBe("suggested");
    expect(stateOf(pva({}))).toBe("matched-full");
    expect(stateOf(pva({ actual: actual({ unpriced_sessions: 1 }) }))).toBe("matched-partial");
    expect(stateOf(pva({ actual: actual({ cost: null, cost_source: "unknown" }) }))).toBe("matched-partial");
  });
});

describe("rows", () => {
  it("computes deltas only where both sides are known", () => {
    const r = Object.fromEntries(rows(planned, actual()).map((x) => [x.key, x]));
    expect(r.cost.delta).toBeCloseTo(-4);
    expect(r.cost.costNote).toBe("estimated");
    expect(r.kwh.deltaText).toBe("−10 kWh");
    expect(r.stops.deltaText).toBe("+1");
    expect(r.arrival_soc.deltaText).toBe("−5 pts");
    expect(r.duration.deltaText).toBe("+60 min");
  });
  it("never invents a cost: unknown or partial gives no delta", () => {
    const u = rows(planned, actual({ cost: null, cost_source: "unknown" })).find((x) => x.key === "cost")!;
    expect(u.costNote).toBe("unknown");
    expect(u.actual).toBeNull();
    expect(u.delta).toBeNull();
    const p = rows(planned, actual({ unpriced_sessions: 2 })).find((x) => x.key === "cost")!;
    expect(p.costNote).toBe("partial");
    expect(p.delta).toBeNull();
  });
  it("a missing planned figure gives no delta", () => {
    const r = rows({ ...planned, kwh: null }, actual()).find((x) => x.key === "kwh")!;
    expect(r.planned).toBeNull();
    expect(r.delta).toBeNull();
  });
});
