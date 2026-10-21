import { describe, expect, it } from "vitest";
import { ApiError } from "../lib/api";
import { collectInputs, planErrorCopy } from "./plannerHelpers";

describe("planErrorCopy", () => {
  it("names the wait on a quota error", () => {
    const c = planErrorCopy(new ApiError(429, { error: "slow down", class: "plan_quota", retry_after_sec: 600 }));
    expect(c.title).toBe("You've planned a lot in the last hour.");
    expect(c.detail).toContain("about 10 minutes");
  });
  it("says what is missing when the truck's charge is unknown", () => {
    expect(planErrorCopy(new ApiError(400, { error: "vehicle, starting SoC, and pack capacity required" })).title).toBe(
      "We don't know your truck's current charge.",
    );
  });
  it("tells too many days from too many stops", () => {
    expect(planErrorCopy(new ApiError(400, { error: "at most 10 days" })).title).toBe("Trips are limited to 10 days.");
    expect(planErrorCopy(new ApiError(400, { error: "day 2 has more than 10 waypoints" })).detail).toContain("remove a via");
  });
  it("blames the network, not the trip, when the request never arrived", () => {
    expect(planErrorCopy(new TypeError("Failed to fetch")).title).toBe("We couldn't reach Rivolt.");
  });
  it("falls back to the planner being unavailable", () => {
    expect(planErrorCopy(new ApiError(502, "bad gateway")).title).toBe("Rivian's planner isn't answering right now.");
  });
});

describe("saved multi-day plans", () => {
  it("recognises the saved shape and sums day totals", async () => {
    const { isSavedMultiday } = await import("../lib/api");
    const { totalsOf } = await import("./plannerHelpers");
    expect(isSavedMultiday({ kind: "multiday", days: [] })).toBe(true);
    expect(isSavedMultiday({ Routes: [] })).toBe(false);
    expect(isSavedMultiday(undefined)).toBe(false);
    const day = (m: number) => ({ plan: { Routes: [{ TotalDriveDistanceMeters: m, TotalDriveDurationSec: 10, TotalChargingDurationSec: 5 }] } }) as never;
    expect(totalsOf([day(100), day(250), { plan: {} } as never])).toEqual({ distance_meters: 350, drive_duration_sec: 20, charging_duration_sec: 10 });
  });
});

describe("collectInputs: the night's hookup", () => {
  it("is saved with the night, so a reload knows which hookup the 7.2 kW was", () => {
    const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
    const saved = collectInputs({
      origin: pt("A", 1, 1),
      destination: pt("B", 2, 2),
      extraStops: [pt("Mono Lake Campground", 37.99, -119.11)],
      overnightFlags: [true],
      maxOvernightSoCPct: 80,
      overnightL2Kw: 7.2,
      overnightDepartureLocal: "08:00",
      overnightSettings: [{ lat: 37.99, lon: -119.11, until: "08:00", l2_kw: 7.2, max_soc_pct: 80, hookup: "nema_14_50" }],
      targetSoc: "20",
      startingSoc: "",
      driveMode: "EVERYDAY",
      hasAdapter: false,
      departureAt: "",
    });
    expect(saved.overnight_settings).toEqual([{ lat: 37.99, lon: -119.11, until: "08:00", l2_kw: 7.2, max_soc_pct: 80, hookup: "nema_14_50" }]);
  });
});
