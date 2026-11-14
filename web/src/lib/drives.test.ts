import { describe, it, expect } from "vitest";
import type { Drive } from "./api";
import { collapseRoundTrips } from "./drives";

// Minimal Drive factory — only the fields collapseRoundTrips reads
// matter; the rest get harmless defaults.
let seq = 0;
function drive(o: Partial<Drive>): Drive {
  seq += 1;
  return {
    ID: `d${seq}`,
    VehicleID: "v1",
    StartedAt: "2026-07-16T10:00:00Z",
    EndedAt: "2026-07-16T10:20:00Z",
    StartSoCPct: 80,
    EndSoCPct: 75,
    StartOdometerMi: 0,
    EndOdometerMi: 0,
    DistanceMi: 5,
    StartLat: 0,
    StartLon: 0,
    EndLat: 0,
    EndLon: 0,
    MaxSpeedMph: 60,
    AvgSpeedMph: 30,
    EnergyUsedKWh: 2,
    Source: "test",
    ...o,
  };
}

// Reference points (well separated so cross-matches never fire by
// accident): home A, destination B, an unrelated origin X.
const A = { lat: 37.7749, lon: -122.4194 };
const B = { lat: 37.8044, lon: -122.2712 };
const X = { lat: 37.3382, lon: -121.8863 };
// A's recorded start drifted ~400 m north — simulates the dropped
// first-60-90s of telemetry that pushes StartLat/Lon off the true origin.
const A_DRIFT = { lat: 37.7749 + 0.0036, lon: -122.4194 };

const RADIUS = 200; // metres — the app default
const GAP = 90; // minutes — the app default

describe("collapseRoundTrips origin anchoring", () => {
  it("merges an out-and-back whose outbound start drifted, using the prior drive's end as the origin", () => {
    // Newest-first (ListRecent contract). Chronologically:
    //   prev:  X → A   (arrives home, End reliable = A)
    //   leg1:  A(drift) → B   (outbound; recorded start off by ~400 m)
    //   leg2:  B → A   (return home)
    const prev = drive({
      StartedAt: "2026-07-16T08:00:00Z",
      EndedAt: "2026-07-16T08:30:00Z",
      StartLat: X.lat, StartLon: X.lon,
      EndLat: A.lat, EndLon: A.lon,
    });
    const leg1 = drive({
      StartedAt: "2026-07-16T09:00:00Z",
      EndedAt: "2026-07-16T09:20:00Z",
      StartLat: A_DRIFT.lat, StartLon: A_DRIFT.lon,
      EndLat: B.lat, EndLon: B.lon,
    });
    const leg2 = drive({
      StartedAt: "2026-07-16T09:40:00Z",
      EndedAt: "2026-07-16T10:00:00Z",
      StartLat: B.lat, StartLon: B.lon,
      EndLat: A.lat, EndLon: A.lon,
    });

    const out = collapseRoundTrips([leg2, leg1, prev], RADIUS, GAP);
    // prev stays on its own; leg1+leg2 collapse into one round-trip row.
    // Output is newest-first; a merged row keeps the chain head's ID.
    expect(out).toHaveLength(2);
    expect(out.map((d) => d.ID)).toEqual([leg1.ID, prev.ID]);
    const roundTrip = out[0];
    expect(roundTrip.EndLat).toBeCloseTo(A.lat, 4); // ends back home
    expect(roundTrip.DistanceMi).toBeCloseTo(10, 5); // both legs summed
    // The merged row says how many recorded drives it holds; a single
    // drive carries no count.
    expect(roundTrip.round_trip_legs).toBe(2);
    expect(out[1].round_trip_legs).toBeUndefined();
  });

  it("still merges a clean round trip with an accurate start and no predecessor", () => {
    const leg1 = drive({
      StartedAt: "2026-07-16T09:00:00Z",
      EndedAt: "2026-07-16T09:20:00Z",
      StartLat: A.lat, StartLon: A.lon,
      EndLat: B.lat, EndLon: B.lon,
    });
    const leg2 = drive({
      StartedAt: "2026-07-16T09:40:00Z",
      EndedAt: "2026-07-16T10:00:00Z",
      StartLat: B.lat, StartLon: B.lon,
      EndLat: A.lat, EndLon: A.lon,
    });
    const out = collapseRoundTrips([leg2, leg1], RADIUS, GAP);
    expect(out).toHaveLength(1);
    expect(out[0].DistanceMi).toBeCloseTo(10, 5);
  });

  it("does not merge a one-way A→B→C trip that never returns", () => {
    const leg1 = drive({
      StartedAt: "2026-07-16T09:00:00Z",
      EndedAt: "2026-07-16T09:20:00Z",
      StartLat: A.lat, StartLon: A.lon,
      EndLat: B.lat, EndLon: B.lon,
    });
    const leg2 = drive({
      StartedAt: "2026-07-16T09:40:00Z",
      EndedAt: "2026-07-16T10:00:00Z",
      StartLat: B.lat, StartLon: B.lon,
      EndLat: X.lat, EndLon: X.lon,
    });
    const out = collapseRoundTrips([leg2, leg1], RADIUS, GAP);
    expect(out).toHaveLength(2);
  });

  it("does not merge when the parked gap exceeds maxGapMinutes", () => {
    const leg1 = drive({
      StartedAt: "2026-07-16T09:00:00Z",
      EndedAt: "2026-07-16T09:20:00Z",
      StartLat: A.lat, StartLon: A.lon,
      EndLat: B.lat, EndLon: B.lon,
    });
    const leg2 = drive({
      // 3 hours after leg1 ends — beyond the 90-min gap.
      StartedAt: "2026-07-16T12:20:00Z",
      EndedAt: "2026-07-16T12:40:00Z",
      StartLat: B.lat, StartLon: B.lon,
      EndLat: A.lat, EndLon: A.lon,
    });
    const out = collapseRoundTrips([leg2, leg1], RADIUS, GAP);
    expect(out).toHaveLength(2);
  });
});

describe("round-trip legs", () => {
  const at = (hhmm: string) => `2026-07-16T${hhmm}:00Z`;
  const home = A;
  const stopA = B;
  const stopB = X;
  // home -> A, a zero-distance stub at A, A -> B, B -> home.
  const chain = () => [
    drive({ ID: "l3", StartedAt: at("15:06"), EndedAt: at("15:13"), DistanceMi: 3.3, StartLat: stopB.lat, StartLon: stopB.lon, EndLat: home.lat, EndLon: home.lon }),
    drive({ ID: "l2", StartedAt: at("14:37"), EndedAt: at("14:47"), DistanceMi: 3.3, StartLat: stopA.lat, StartLon: stopA.lon, EndLat: stopB.lat, EndLon: stopB.lon }),
    drive({ ID: "stub", StartedAt: at("14:30"), EndedAt: at("14:31"), DistanceMi: 0, StartLat: stopA.lat, StartLon: stopA.lon, EndLat: stopA.lat, EndLon: stopA.lon }),
    drive({ ID: "l1", StartedAt: at("14:17"), EndedAt: at("14:24"), DistanceMi: 3.1, StartLat: home.lat, StartLon: home.lon, EndLat: stopA.lat, EndLon: stopA.lon }),
  ];

  it("lists the real legs in order and counts three, not four", () => {
    const out = collapseRoundTrips(chain(), RADIUS, GAP);
    expect(out).toHaveLength(1);
    expect(out[0].round_trip_legs).toBe(3);
    expect(out[0].round_trip_leg_list?.map((l) => l.ID)).toEqual(["l1", "l2", "l3"]);
    expect(out[0].DistanceMi).toBeCloseTo(9.7, 5);
  });

  it("never counts below one, and lists no legs, for a chain of stubs", () => {
    const stubs = [0, 1].map((i) =>
      drive({ ID: `s${i}`, StartedAt: at(i ? "14:40" : "14:30"), EndedAt: at(i ? "14:41" : "14:31"), DistanceMi: 0, StartLat: home.lat, StartLon: home.lon, EndLat: home.lat, EndLon: home.lon }),
    );
    for (const row of collapseRoundTrips(stubs, RADIUS, GAP)) {
      expect(row.round_trip_legs ?? 1).toBeGreaterThanOrEqual(1);
      expect(row.round_trip_leg_list).toBeUndefined();
    }
  });

  it("measures the parked time to the next real leg, absorbing the stub, and none after the last", () => {
    const legs = collapseRoundTrips(chain(), RADIUS, GAP)[0].round_trip_leg_list!;
    expect(legs.map((l) => l.parkedMinAfter)).toEqual([13, 19, null]);
  });
});
