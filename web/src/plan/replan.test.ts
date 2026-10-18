import { describe, expect, it } from "vitest";
import type { SavedTripActive, TripPlanMultidayDay, TripPlanMultidayRequest } from "../lib/api";
import { mergeReplan, replanRequest } from "./replan";

const pt = (label: string) => ({ lat: 1, lon: 2, label });
const base: TripPlanMultidayRequest = {
  vehicle_id: "v1",
  starting_soc: 90,
  start_at: "2026-10-20T13:00:00Z",
  tz: "America/Chicago",
  origin: pt("A"),
  days: [
    { vias: [pt("v1"), pt("v2")], end: pt("N1"), depart_at: "2026-10-20T13:00:00Z", overnight: { l2_kw: 7, until: "08:00", max_soc_pct: 80, name: "N1" } },
    { end: pt("N2"), overnight: { l2_kw: 7, until: "08:00", max_soc_pct: 80, name: "N2" } },
    { end: pt("Z") },
  ],
};
const active = (over: Partial<SavedTripActive>): SavedTripActive => ({
  version: 1, saved_trip_id: "s", kind: "multiday", day_count: 3, state: "active", current_day: 1, remaining_days: 3,
  position: { lat: 1, lon: 2, soc: 61, at: "2026-10-20T16:00:00Z" }, phase: "driving", ...over,
});

describe("replanRequest", () => {
  it("plans from the truck, dropping the vias already passed and the truck and charge it was first planned with", () => {
    const r = replanRequest(base, active({ vias_passed: [1] }), new Date("2026-10-20T16:05:00Z"))!;
    expect(r.fromDay).toBe(1);
    expect(r.passed).toEqual(["v1"]);
    expect(r.req.origin_from_vehicle).toBe(true);
    expect(r.req.days[0].vias).toEqual([pt("v2")]);
    expect(r.req.days[0].depart_at).toBeUndefined();
    expect(r.req).not.toHaveProperty("vehicle_id");
    expect(r.req).not.toHaveProperty("starting_soc");
    expect(r.req.start_at).toBe("2026-10-20T16:05:00.000Z");
    expect(r.soc).toBe(61);
  });
  it("on the night, starts the next day when it leaves with the projected charge", () => {
    const r = replanRequest(base, active({ phase: "overnight", next_day: 2, next_depart_at: "2026-10-21T13:00:00Z", projected_soc: 80 }))!;
    expect(r.fromDay).toBe(2);
    expect(r.req.days).toHaveLength(2);
    expect(r.req.start_at).toBe("2026-10-21T13:00:00Z");
    expect(r.req.starting_soc).toBe(80);
    expect(r.soc).toBe(80);
  });
  it("has nothing to plan when no day is current", () => {
    expect(replanRequest(base, active({ current_day: null }))).toBeNull();
  });
});

describe("mergeReplan", () => {
  const day = (index: number) => ({ index, plan: { Routes: [{ TotalDriveDistanceMeters: 100, TotalDriveDurationSec: 10, TotalChargingDurationSec: 1 }] } }) as unknown as TripPlanMultidayDay;
  it("keeps the earlier days and numbers the new ones from the day they replace", () => {
    const m = mergeReplan([day(1), day(2), day(3)], 2, { days: [day(1), day(2)], total: { distance_meters: 0, drive_duration_sec: 0, charging_duration_sec: 0 }, failed_day: 2 });
    expect(m.days.map((d) => d.index)).toEqual([1, 2, 3]);
    expect(m.failed_day).toBe(3);
    expect(m.total.distance_meters).toBe(300);
  });
});
