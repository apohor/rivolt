import { describe, expect, it } from "vitest";
import type { PlanVsActual, PvaMetric } from "./api";
import { costHeader, rows, stateOf, stopLabel, topCandidate } from "./planVsActual";

const m = (metric: PvaMetric["metric"], planned: number | null, actual: number | null, o: Partial<PvaMetric> = {}): PvaMetric => ({
  metric, unit: "", planned, actual, delta: planned != null && actual != null ? actual - planned : null, reasons: [], ...o,
});
const cmp = (metrics: PvaMetric[]): NonNullable<PlanVsActual["comparison"]> => ({
  currency: "USD", plan_from: "snapshot", leg: { started_at: "2026-10-10T00:00:00Z", arrived_at: "2026-10-10T10:00:00Z" }, metrics, stops: [],
});
const pva = (o: Partial<PlanVsActual> = {}): PlanVsActual => ({
  version: 1, saved_trip_id: "s", status: "linked", link: null, candidates: [], comparison: cmp([m("cost", 50, 46, { actual_complete: true })]), ...o,
});

describe("stateOf", () => {
  it("covers each state", () => {
    expect(stateOf(pva({ status: "none", comparison: null }))).toBe("none");
    expect(stateOf(pva({ status: "suggested", candidates: [{ trip_id: "t", started_at: "", ended_at: "", confidence: 0.9, band: "high", signals: {} }] }))).toBe("suggested");
    expect(stateOf(pva())).toBe("linked-full");
    expect(stateOf(pva({ comparison: cmp([m("cost", 50, null, { actual_complete: false, actual_known: 20, unpriced_sessions: 2 })]) }))).toBe("linked-partial");
  });
  it("picks the first candidate as the top one", () => {
    const c = { trip_id: "t1", started_at: "", ended_at: "", confidence: 0.9, band: "high" as const, signals: {} };
    expect(topCandidate(pva({ candidates: [c, { ...c, trip_id: "t2" }] }))?.trip_id).toBe("t1");
    expect(topCandidate(pva({ candidates: [] }))).toBeNull();
  });
});

describe("rows", () => {
  it("formats deltas and tones: cheaper good, extra time watch, stops neutral", () => {
    const r = Object.fromEntries(
      rows(cmp([m("cost", 50, 46), m("energy_kwh", 150, 140), m("dcfc_stops", 3, 4), m("duration", 600, 660)])).map((x) => [x.key, x]),
    );
    expect(r.cost.deltaText).toMatch(/^−.*4/);
    expect(r.cost.tone).toBe("good");
    expect(r.energy_kwh.deltaText).toBe("−10 kWh");
    expect(r.dcfc_stops.deltaText).toBe("+1");
    expect(r.dcfc_stops.tone).toBe("neutral");
    expect(r.duration.deltaText).toBe("+60 min");
    expect(r.duration.tone).toBe("watch");
  });
  it("never shows an incomplete cost as the actual", () => {
    const r = rows(cmp([m("cost", 50, null, { actual_complete: false, actual_known: 21.1, unpriced_sessions: 2 })]))[0];
    expect(r.actual).toBeNull();
    expect(r.deltaText).toBeNull();
    expect(r.incomplete).toEqual({ known: 21.1, unpriced: 2 });
  });
  it("marks a cost with network estimates as estimated", () => {
    const r = rows(cmp([m("cost", 50, 46, { actual_complete: true, actual_sources: { rivian: 20, network_estimate: 26 } })]))[0];
    expect(r.estimated).toBe(true);
  });
  it("caps reasons at two and uses placeholder-free copy only as a fallback", () => {
    const r = rows(
      cmp([
        m("cost", 50, 46, {
          reasons: [
            { code: "extra_stop", amount: 9 },
            { code: "rate_differed", amount: 5 }, // needs placeholders and has no detail: dropped
            { code: "skipped_stop", amount: -2 },
            { code: "estimated" },
          ],
        }),
      ]),
    )[0];
    expect(r.reasons).toEqual(["You made an extra fast-charge stop.", "You skipped a planned stop."]);
  });
  it("orders reasons by amount (largest first), informational after", () => {
    const r = rows(
      cmp([
        m("cost", 50, 46, {
          reasons: [
            { code: "estimated", detail: "Part of this is estimated, not billed" },
            { code: "rate_differed", amount: -1, detail: "small" },
            { code: "extra_stop", amount: 9, detail: "big" },
          ],
        }),
      ]),
    )[0];
    expect(r.reasons).toEqual(["big", "small"]);
  });
  it("has no delta when the server gave none", () => {
    const r = rows(cmp([m("energy_kwh", 150, null)]))[0];
    expect(r.actual).toBeNull();
    expect(r.deltaText).toBeNull();
  });
});

describe("costHeader", () => {
  const h = (metric: PvaMetric) => costHeader(cmp([metric]));
  it("covers on plan, over, under, not final, no plan cost, currency", () => {
    expect(h(m("cost", 50, 52, { actual_complete: true }))).toMatch(/^On plan\./);
    expect(h(m("cost", 50, 70, { actual_complete: true }))).toMatch(/over plan/);
    expect(h(m("cost", 50, 30, { actual_complete: true }))).toMatch(/under plan/);
    expect(h(m("cost", 50, null, { actual_complete: false, actual_known: 20, unpriced_sessions: 2 }))).toMatch(
      /^Not final yet\..*2 charges aren't priced yet\./,
    );
    expect(h(m("cost", null, 40, { actual_complete: true }))).toMatch(/^No cost in the plan\./);
    expect(h(m("cost", 50, null, { reasons: [{ code: "currency_mismatch" }] }))).toMatch(/^Can't compare\./);
  });
  it("says estimated when any part is a network estimate", () => {
    expect(h(m("cost", 50, 70, { actual_complete: true, actual_sources: { network_estimate: 10 } }))).toMatch(/\(estimated\)/);
  });
});

describe("stopLabel", () => {
  it("labels nearby and AC extras honestly", () => {
    expect(stopLabel({ match: "nearby" })).toBe("Nearby, network not known");
    expect(stopLabel({ match: "extra", actual: { kind: "ac" } })).toBe("Extra AC charge (not in the cost)");
    expect(stopLabel({ match: "extra", actual: { kind: "dcfc" } })).toBe("Extra stop");
  });
});
