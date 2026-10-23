import { describe, it, expect } from "vitest";
import type { Drive } from "./api";
import { collapseRoundTrips, driveHasTrailer } from "./drives";
import { driveStats } from "./analytics";
import { choiceOf, overrideOf } from "../components/TrailerControl";

let seq = 0;
function drive(o: Partial<Drive>): Drive {
  seq += 1;
  return {
    ID: `t${seq}`, VehicleID: "v1",
    StartedAt: "2026-07-16T10:00:00Z", EndedAt: "2026-07-16T10:20:00Z",
    StartSoCPct: 80, EndSoCPct: 70, StartOdometerMi: 0, EndOdometerMi: 0,
    DistanceMi: 20, StartLat: 0, StartLon: 0, EndLat: 0, EndLon: 0,
    MaxSpeedMph: 60, AvgSpeedMph: 30, EnergyUsedKWh: 10, Source: "test", ...o,
  };
}

describe("driveHasTrailer", () => {
  it("is the user's correction, else what the truck reported, else null", () => {
    expect(driveHasTrailer({ Trailer: true, TrailerOverride: false })).toBe(false);
    expect(driveHasTrailer({ Trailer: false, TrailerOverride: true })).toBe(true);
    expect(driveHasTrailer({ Trailer: true, TrailerOverride: null })).toBe(true);
    expect(driveHasTrailer({})).toBeNull();
  });
});

describe("driveStats with trailer drives", () => {
  it("leaves them out of the efficiency figures, keeps them in the counts, and counts them", () => {
    const s = driveStats([
      drive({}),
      drive({ Trailer: true, DistanceMi: 20, EnergyUsedKWh: 20 }),
      // recorded as trailer but the user corrected it: counts as a plain drive
      drive({ Trailer: true, TrailerOverride: false }),
      // recorded as none but the user says trailer: left out
      drive({ Trailer: false, TrailerOverride: true }),
    ]);
    expect(s.count).toBe(4);
    expect(s.miles).toBe(80);
    expect(s.trailerExcluded).toBe(2);
    expect(s.energyUsedKWh).toBe(20);
    expect(s.milesForEnergy).toBe(40);
    expect(s.miPerKWh).toBe(2);
  });
  it("reports none when no drive has a trailer", () => {
    expect(driveStats([drive({}), drive({ Trailer: false })]).trailerExcluded).toBe(0);
  });
});

describe("a round trip's trailer", () => {
  const A = { lat: 37.7749, lon: -122.4194 };
  const B = { lat: 37.8044, lon: -122.2712 };
  const legs = (t1: Partial<Drive>, t2: Partial<Drive>) => {
    const l1 = drive({ StartedAt: "2026-07-16T09:00:00Z", EndedAt: "2026-07-16T09:20:00Z", StartLat: A.lat, StartLon: A.lon, EndLat: B.lat, EndLon: B.lon, ...t1 });
    const l2 = drive({ StartedAt: "2026-07-16T09:40:00Z", EndedAt: "2026-07-16T10:00:00Z", StartLat: B.lat, StartLon: B.lon, EndLat: A.lat, EndLon: A.lon, ...t2 });
    return collapseRoundTrips([l2, l1], 300, 90)[0];
  };
  it("has one when any leg did, and carries no correction of its own", () => {
    const m = legs({ Trailer: false }, { Trailer: true, TrailerOverride: null });
    expect(m.round_trip_legs).toBe(2);
    expect(driveHasTrailer(m)).toBe(true);
    expect(m.TrailerOverride).toBeNull();
  });
  it("honours a corrected leg, and is none when every leg is known to have none", () => {
    expect(driveHasTrailer(legs({ Trailer: true, TrailerOverride: false }, { Trailer: false }))).toBe(false);
    expect(driveHasTrailer(legs({}, {}))).toBeNull();
  });
});

describe("TrailerControl choices", () => {
  it("maps the select to the override and back", () => {
    expect(overrideOf("auto")).toBeNull();
    expect(overrideOf("yes")).toBe(true);
    expect(overrideOf("no")).toBe(false);
    expect(choiceOf(null)).toBe("auto");
    expect(choiceOf(undefined)).toBe("auto");
    expect(choiceOf(true)).toBe("yes");
    expect(choiceOf(false)).toBe("no");
  });
});
