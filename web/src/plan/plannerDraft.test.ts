import { beforeEach, describe, expect, it } from "vitest";
import { DRAFT_KEY, DRAFT_MAX_AGE_MS, savePlannerDraft, takePlannerDraft, type PlannerDraft } from "./plannerDraft";
import { noRoutePrefs } from "./routePrefs";

const draft = (over: Partial<PlannerDraft> = {}): PlannerDraft => ({
  origin: { lat: 30.2, lon: -97.7, label: "Austin" },
  destination: { lat: 32.7, lon: -96.8, label: "Dallas" },
  extraStops: [{ lat: 31.5, lon: -97.1, label: "Waco" }],
  overnightFlags: [true],
  overnightOpts: { "31.5,-97.1": { l2Kw: 7, until: "08:00", maxSocPct: 80 } },
  maxOvernightSoCPct: 80,
  overnightDepartureLocal: "08:00",
  overnightL2Kw: 7,
  targetSoc: "15",
  startingSoc: "90",
  driveMode: "DISTANCE",
  hasAdapter: true,
  departureAt: "2026-11-10T08:00",
  routePrefs: { ...noRoutePrefs, avoidTolls: true },
  ...over,
});

describe("plannerDraft", () => {
  beforeEach(() => window.sessionStorage.clear());

  it("round-trips the form once and removes it", () => {
    savePlannerDraft(draft(), 1000);
    expect(takePlannerDraft(2000)).toEqual(draft());
    expect(takePlannerDraft(2000)).toBeNull();
  });

  it("does not save an empty form", () => {
    savePlannerDraft(draft({ origin: null, destination: null, extraStops: [], overnightFlags: [] }), 1000);
    expect(window.sessionStorage.getItem(DRAFT_KEY)).toBeNull();
  });

  it("drops a draft older than ten minutes, or from the future", () => {
    savePlannerDraft(draft(), 1000);
    expect(takePlannerDraft(1000 + DRAFT_MAX_AGE_MS + 1)).toBeNull();
    savePlannerDraft(draft(), 5000);
    expect(takePlannerDraft(1000)).toBeNull();
  });

  it("ignores garbage and other versions without throwing", () => {
    window.sessionStorage.setItem(DRAFT_KEY, "{not json");
    expect(takePlannerDraft()).toBeNull();
    window.sessionStorage.setItem(DRAFT_KEY, JSON.stringify({ v: 99, at: Date.now(), draft: draft() }));
    expect(takePlannerDraft()).toBeNull();
    window.sessionStorage.setItem(DRAFT_KEY, JSON.stringify({ v: 1, at: Date.now(), draft: { extraStops: "x" } }));
    expect(takePlannerDraft()).toBeNull();
  });
});
