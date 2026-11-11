import { describe, expect, it } from "vitest";
import { ApiError } from "../lib/api";
import { collectInputs, planErrorCopy, retryOnceAfterReconnect } from "./plannerHelpers";

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
  it("says a truck must be connected when none is linked, even with a starting charge typed", () => {
    const c = planErrorCopy(new ApiError(400, { error: "vehicle_id required (no vehicle linked to this user)" }));
    expect(c.title).toBe("Connect your Rivian to plan a trip.");
    expect(c.detail).toContain("needs a connected truck");
    expect(c.detail).not.toMatch(/enter a starting charge/i);
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
  it("says reconnecting, not an outage, on a 503 that says when to retry (the vehicle's owner did not answer)", () => {
    const e = new ApiError(503, { error: "the vehicle's owner did not answer", class: "outage" });
    e.retryAfter = 5;
    const c = planErrorCopy(e);
    expect(c.title).toBe("Reconnecting to Rivian.");
    expect(c.detail).toContain("Try again in a moment");
    expect(c.detail).not.toMatch(/Rivian|minutes/);
  });
  it("keeps the planner copy for a 503 without Retry-After (a Rivian outage or a pause)", () => {
    expect(planErrorCopy(new ApiError(503, { error: "outage", class: "outage" })).title).toBe("Rivian's planner isn't answering right now.");
  });
});

describe("retryOnceAfterReconnect", () => {
  const reconnecting = (ra: number) => Object.assign(new ApiError(503, { error: "the vehicle's owner did not answer", class: "outage" }), { retryAfter: ra });
  it("plans once more after Retry-After and returns that answer", async () => {
    const waits: number[] = [];
    let n = 0;
    const got = await retryOnceAfterReconnect(async () => (n++ === 0 ? Promise.reject(reconnecting(5)) : "plan"), async (ms) => void waits.push(ms));
    expect(got).toBe("plan");
    expect(n).toBe(2);
    expect(waits).toEqual([5_000]);
  });
  it("retries only once (a plan spends the shared quota) and caps the wait at 10 s", async () => {
    const waits: number[] = [];
    let n = 0;
    await expect(retryOnceAfterReconnect(async () => { n++; throw reconnecting(60); }, async (ms) => void waits.push(ms))).rejects.toMatchObject({ status: 503 });
    expect(n).toBe(2);
    expect(waits).toEqual([10_000]);
  });
  it("does not retry other errors", async () => {
    let n = 0;
    await expect(retryOnceAfterReconnect(async () => { n++; throw new ApiError(503, "outage"); }, async () => {})).rejects.toBeInstanceOf(ApiError);
    expect(n).toBe(1);
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
