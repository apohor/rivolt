import { describe, expect, it } from "vitest";
import type { VehicleState } from "./api";
import { advanceTripDistance, clock, computeNow, formatEta, freshnessLine, nowAlerts, statusPill, type NowInput } from "./nowState";

const NOW = Date.parse("2026-10-10T12:00:00Z");
const HOME = { latitude: 40, longitude: -90 };

function st(p: Partial<VehicleState> = {}): VehicleState {
  return {
    at: new Date(NOW - 5_000).toISOString(),
    battery_level_pct: 60,
    distance_to_empty: 160,
    gear: "P",
    charger_state: "",
    charger_status: "",
    charger_power_kw: 0,
    charge_target_pct: 80,
    latitude: 40,
    longitude: -90,
    speed_kph: 0,
    power_state: "ready",
    odometer_km: 1000,
    ...p,
  } as VehicleState;
}
function inp(p: Partial<VehicleState> = {}, o: Partial<NowInput> = {}): NowInput {
  return {
    state: st(p),
    nowMs: NOW,
    asleepSinceMs: null,
    socAtChargeStart: null,
    peakKw: null,
    packKwh: 100,
    homeRate: 0.11,
    home: HOME,
    ...o,
  };
}
const PLUG = "chrgr_sts_connected_charging";

describe("computeNow", () => {
  it("never shows kW while parked or driving (stale charger kW, NQ3)", () => {
    expect(computeNow(inp({ charger_power_kw: 7.5 })).powerKw).toBeNull();
    expect(computeNow(inp({ gear: "D", charger_power_kw: 7.5, speed_kph: 50 })).powerKw).toBeNull();
  });
  it("uses gear, not speed, for driving (NQ10)", () => {
    expect(computeNow(inp({ gear: "P", speed_kph: 10 })).kind).toBe("parked");
    expect(computeNow(inp({ gear: "D", speed_kph: 0 })).kind).toBe("driving");
  });
  it("shows Starting at 0 kW while charging (NQ4)", () => {
    const v = computeNow(inp({ charger_status: PLUG, charger_state: "charging_active", charger_power_kw: 0 }));
    expect(v.kind).toBe("charging");
    expect(v.powerKw).toBeNull();
    expect(v.starting).toBe(true);
  });
  it("treats a completed plug-in as plugged, not charging", () => {
    const v = computeNow(inp({ charger_status: PLUG, charger_state: "charging_complete", battery_level_pct: 80 }));
    expect(v.kind).toBe("plugged");
    expect(v.atLimit).toBe(true);
  });
  it("debounces sleep for 2 minutes", () => {
    const p = { power_state: "sleep" };
    expect(computeNow(inp(p, { asleepSinceMs: NOW - 30_000 })).kind).toBe("parked");
    expect(computeNow(inp(p, { asleepSinceMs: NOW - 180_000 })).kind).toBe("asleep");
  });
  it("goes stale then offline by state-specific limits", () => {
    const old = (s: number) => ({ at: new Date(NOW - s * 1000).toISOString() });
    expect(computeNow(inp({ gear: "D", ...old(60) })).stale).toBe(true);
    expect(computeNow(inp({ gear: "D", ...old(200) })).kind).toBe("offline");
    // Awake parked: stale after 2 min (p99 gap is 8 s).
    expect(computeNow(inp(old(90))).stale).toBe(false);
    expect(computeNow(inp(old(130))).stale).toBe(true);
    // Asleep stays lenient: the gate parks subscriptions, so a healthy
    // sleeping truck may be silent for a long while. Stale only after 30 min.
    const asleep = (age: number) =>
      computeNow(inp({ power_state: "sleep", ...old(age) }, { asleepSinceMs: NOW - 7_200_000 }));
    expect(asleep(200).stale).toBe(false);
    expect(asleep(600).stale).toBe(false);
    expect(asleep(1500).stale).toBe(false);
    expect(asleep(1900).stale).toBe(true);
    expect(asleep(1500).kind).toBe("asleep");
    expect(computeNow(inp(old(2000))).kind).toBe("offline");
  });
  it("a plugged-in asleep truck uses the asleep limits, an awake one the 2-min limit", () => {
    const plugged = (power_state: string, ageSec: number, server?: object) =>
      computeNow(
        inp(
          { charger_status: PLUG, charger_state: "charging_complete", power_state, at: new Date(NOW - ageSec * 1000).toISOString() },
          server ? { server: server as NonNullable<NowInput["server"]> } : {},
        ),
      );
    const srv = (asleep: boolean) => ({
      version: 1, at: null, location_fix_at: null, connection: "live", state: "plugged", state_since: null,
      soc_at_state_start: null, tire_pressure_min_bar: null, failed_sleeps_6h: 0, charge: null, asleep,
    });
    // 4 min old, asleep: no pill (server flag, and client fallback).
    for (const v of [plugged("sleep", 240, srv(true)), plugged("sleep", 240)]) {
      expect(v.kind).toBe("plugged");
      expect(v.stale).toBe(false);
    }
    // 4 min old, awake: stale pill.
    for (const v of [plugged("ready", 240, srv(false)), plugged("ready", 240)]) {
      expect(v.kind).toBe("plugged");
      expect(v.stale).toBe(true);
    }
    // The asleep limits: stale after 30 min, offline after 45.
    expect(plugged("sleep", 1900, srv(true)).stale).toBe(true);
    expect(plugged("sleep", 2800, srv(true)).kind).toBe("offline");
    // The server flag wins over a client power_state that disagrees.
    expect(plugged("ready", 240, srv(true)).stale).toBe(false);
  });
  it("plugged + asleep: the /now frame (10 s) beats a 9-min-old /api/state; no Last-seen pill, Updated is ~now", () => {
    const server = {
      version: 1, at: new Date(NOW - 10_000).toISOString(), location_fix_at: null, connection: "live", state: "plugged",
      state_since: null, soc_at_state_start: null, tire_pressure_min_bar: null, failed_sleeps_6h: 0, charge: null, asleep: true,
    } as NonNullable<NowInput["server"]>;
    const v = computeNow(
      inp(
        { charger_status: PLUG, charger_state: "charging_complete", power_state: "sleep", at: new Date(NOW - 9 * 60_000).toISOString() },
        { server },
      ),
    );
    expect(v.kind).toBe("plugged");
    expect(v.ageSec).toBe(10);
    expect(v.stale).toBe(false);
    expect(statusPill(v).text).toBe("Asleep");
    expect(freshnessLine(v)).toContain("Updated just now");
    // Even with /now itself 5 min old (a normal ~3-min sleep cadence)
    // a sleeping plugged-in truck shows no "Last seen" pill.
    const old = computeNow(
      inp(
        { charger_status: PLUG, charger_state: "charging_complete", power_state: "sleep", at: new Date(NOW - 9 * 60_000).toISOString() },
        { server: { ...server, at: new Date(NOW - 300_000).toISOString() } },
      ),
    );
    expect(statusPill(old).text).toBe("Asleep");
    // An awake plugged-in truck with the same ages is stale.
    const awake = computeNow(
      inp(
        { charger_status: PLUG, charger_state: "charging_complete", power_state: "ready", at: new Date(NOW - 9 * 60_000).toISOString() },
        { server: { ...server, asleep: false, at: new Date(NOW - 300_000).toISOString() } },
      ),
    );
    expect(statusPill(awake).tone).toBe("warn");
  });
  it("derives ETA, energy and home cost from vehicle state alone", () => {
    const v = computeNow(
      inp(
        { charger_status: PLUG, charger_state: "charging_active", charger_power_kw: 10, battery_level_pct: 50 },
        { socAtChargeStart: 40 },
      ),
    );
    expect(v.chargeFlavor).toBe("home_l2");
    expect(v.etaHours).toBeCloseTo(3); // 30% of 100 kWh at 10 kW
    expect(v.energyKwh).toBeCloseTo(10);
    expect(v.costSoFar?.amount).toBeCloseTo(1.1);
  });
  it("prefers the truck's own time left when it is valid and plausible, else ours labelled estimate", () => {
    const charging = { charger_status: PLUG, charger_state: "charging_active", charger_power_kw: 10, battery_level_pct: 50 };
    const ours = computeNow(inp(charging));
    expect(ours.etaSource).toBe("estimate");
    expect(ours.etaHours).toBeCloseTo(3);
    // The truck says 3 h 20 min (a taper): shown as the truck's, plainly.
    const truck = computeNow(inp({ ...charging, time_to_end_mins: 200, charging_time_estimation_validity: "valid" }));
    expect(truck.etaSource).toBe("truck");
    expect(truck.etaHours).toBeCloseTo(200 / 60);
    expect(formatEta(truck.etaHours ?? 0, NOW, false)).toBe("3 h 20 m");
    expect(formatEta(ours.etaHours ?? 0, NOW)).toBe("~3 h");
    // Invalid, or implausibly short (under a quarter of ours): ours, with "est.".
    for (const bad of [{ time_to_end_mins: 200, charging_time_estimation_validity: "invalid" }, { time_to_end_mins: 20, charging_time_estimation_validity: "valid" }]) {
      const v = computeNow(inp({ ...charging, ...bad }));
      expect(v.etaSource).toBe("estimate");
      expect(v.etaHours).toBeCloseTo(3);
    }
    // Not charging: no time left from either.
    expect(computeNow(inp({ charger_status: PLUG, charger_state: "charging_complete", time_to_end_mins: 30 })).etaSource).toBeNull();
  });
  it("away AC has unknown price and a multi-day ETA; DCFC by peak kW", () => {
    const away = { latitude: 41, longitude: -90 };
    const v = computeNow(
      inp({ charger_status: PLUG, charger_state: "charging_active", charger_power_kw: 1.4, battery_level_pct: 20, ...away }, { peakKw: 1.4 }),
    );
    expect(v.chargeFlavor).toBe("away_ac");
    expect(v.priceUnknown).toBe(true);
    expect(v.costSoFar).toBeNull();
    expect(v.etaHours).toBeGreaterThan(24);
    const d = computeNow(
      inp({ charger_status: PLUG, charger_state: "charging_active", charger_power_kw: 150, ...away }, { peakKw: 200 }),
    );
    expect(d.chargeFlavor).toBe("dcfc");
    // Tapered below 50 kW with unknown session peak: not labelled AC/unpriced.
    const t = computeNow(
      inp({ charger_status: PLUG, charger_state: "charging_active", charger_power_kw: 30, ...away }, { peakKw: null }),
    );
    expect(t.chargeFlavor).toBeNull();
    expect(t.priceUnknown).toBe(false);
    expect(t.energyKwh).toBeNull();
  });
  it("flags critical SoC and sleep/tire alerts, max two", () => {
    const state = st({ battery_level_pct: 2.6, tire_pressure_fl_bar: 2.0 });
    const view = computeNow({ ...inp(), state });
    expect(view.critical).toBe(true);
    const a = nowAlerts({ view, state, vehicleName: "Tank", failedSleepsToday: 12 });
    expect(a).toHaveLength(2);
    expect(a[0].id).toBe("critical-soc");
    expect(a[1].text).toBe("Tank woke up 12 times today.");
  });
  it("writes the lowest tire's bar to two decimals, as the Tires card does", () => {
    const state = st({ tire_pressure_fl_bar: 2.1 });
    const view = computeNow({ ...inp(), state });
    const a = nowAlerts({ view, state, vehicleName: "Tank", failedSleepsToday: 0 });
    expect(a.map((x) => x.text)).toContain("Lowest tire 2.10 bar.");
  });
  it("hides place when home isn't set", () => {
    const v = computeNow(inp({}, { home: null }));
    expect(v.place).toBe("unknown");
    expect(v.kmFromHome).toBeNull();
  });
});

describe("advanceTripDistance", () => {
  it("never decreases and holds unconfirmed jumps", () => {
    let t = advanceTripDistance(null, 100, 0);
    t = advanceTripDistance(t, 100.01, 3000);
    t = advanceTripDistance(t, 99.9, 6000); // backward step
    expect(t.max).toBe(100.01);
    t = advanceTripDistance(t, 102.4, 7000); // +1.9 mi in 1 s
    expect(t.max).toBe(100.01);
    t = advanceTripDistance(t, 102.5, 10000); // confirmed
    expect(t.max).toBe(102.5);
  });
});

describe("computeNow with a /now snapshot", () => {
  const snap = (o: Partial<NonNullable<NowInput["server"]>> = {}) =>
    ({
      version: 1, at: null, location_fix_at: null, connection: "live", state: "plugged", state_since: null,
      soc_at_state_start: null, tire_pressure_min_bar: null, failed_sleeps_6h: 0, charge: null, ...o,
    }) as NonNullable<NowInput["server"]>;

  it("takes the state from the server", () => {
    const v = computeNow(inp({ power_state: "sleep" }, { server: snap({ state: "plugged" }), asleepSinceMs: NOW - 600_000 }));
    expect(v.kind).toBe("plugged");
  });
  it("falls back to client rules when the server state is unknown", () => {
    const v = computeNow(inp({ gear: "D" }, { server: snap({ state: "unknown" }) }));
    expect(v.kind).toBe("driving");
  });
  it("derives replay, fix time, since and SoC change", () => {
    const v = computeNow(
      inp({ battery_level_pct: 64 }, {
        server: snap({ state: "parked", connection: "replay", location_fix_at: "2026-10-10T08:00:00Z", state_since: "2026-10-10T09:00:00Z", soc_at_state_start: 68 }),
      }),
    );
    expect(v.replay).toBe(true);
    expect(v.locationFixMs).toBe(Date.parse("2026-10-10T08:00:00Z"));
    expect(v.socChange).toBe(-4);
  });
  it("prefers server tire min and 6h failed sleeps in alerts", () => {
    const state = st({ tire_pressure_fl_bar: 2.9 });
    const view = computeNow({ ...inp(), state });
    const a = nowAlerts({ view, state, vehicleName: "Tank", failedSleepsToday: 0, failedSleeps6h: 5, tireMinBar: 2.0 });
    expect(a.map((x) => x.id)).toEqual(["waking", "tire"]);
    expect(a[0].text).toBe("Tank woke up 5 times in the last 6 hours.");
  });
});

describe("statusPill", () => {
  const pill = (p: Partial<VehicleState>, o: Partial<NowInput> = {}) => statusPill(computeNow(inp(p, o)));
  it("is Live only for a fresh frame", () => {
    expect(pill({}).text).toBe("Live");
    expect(pill({ at: new Date(NOW - 90_000).toISOString() }).text).toBe("Live");
  });
  it("never says Live next to an old timestamp", () => {
    const p = pill({ at: new Date(NOW - 4 * 60_000).toISOString() });
    expect(p.text).toBe("Last seen 4 min ago");
    expect(p.tone).toBe("warn"); // an awake truck sends a frame every few seconds
    const w = pill({ at: new Date(NOW - 6 * 60_000).toISOString() });
    expect(w.text).toBe("Last seen 6 min ago");
    expect(w.tone).toBe("warn");
  });
  it("says Asleep for a sleeping truck and Offline when gone", () => {
    expect(pill({ power_state: "sleep", at: new Date(NOW - 3 * 60_000).toISOString() }, { asleepSinceMs: NOW - 600_000 }).text).toBe("Asleep");
    expect(pill({ at: new Date(NOW - 3_600_000).toISOString() }).text).toBe("Offline");
  });
});

describe("freshnessLine", () => {
  const snapFor = (o: object) =>
    ({
      version: 1, at: null, location_fix_at: null, connection: "live", state: "parked", state_since: null,
      soc_at_state_start: null, tire_pressure_min_bar: null, failed_sleeps_6h: 0, charge: null, ...o,
    }) as NonNullable<NowInput["server"]>;
  const line = (server: object, p: Partial<VehicleState> = {}) => freshnessLine(computeNow(inp(p, { server: snapFor(server) })));
  const FIX = "2026-10-10T10:00:00Z";
  const SINCE = "2026-10-10T11:00:00Z";

  it("says the fix came before it parked when the fix predates the state", () => {
    const l = line({ state: "parked", location_fix_at: FIX, state_since: SINCE });
    expect(l).toContain(`last GPS fix ${clock(Date.parse(FIX))}, before it parked`);
    expect(l).toContain(`since ${clock(Date.parse(SINCE))}`);
    expect(l).not.toContain("location as of");
  });
  it("uses the asleep and plugged-in variants", () => {
    const asleep = freshnessLine(
      computeNow(inp({ power_state: "sleep" }, { asleepSinceMs: NOW - 600_000, server: snapFor({ state: "asleep", location_fix_at: FIX, state_since: SINCE }) })),
    );
    expect(asleep).toContain("before it fell asleep");
    const plugged = line({ state: "plugged", location_fix_at: FIX, state_since: SINCE }, { charger_status: "chrgr_sts_connected_not_charging" });
    expect(plugged).toContain("before it plugged in");
  });
  it("keeps the plain wording when the fix is after the state start", () => {
    const l = line({ state: "parked", location_fix_at: "2026-10-10T11:30:00Z", state_since: SINCE });
    expect(l).toContain("location as of");
    expect(l).not.toContain("before it");
  });
  it("says nothing about the fix when it is recent", () => {
    const l = line({ state: "parked", location_fix_at: new Date(NOW - 60_000).toISOString(), state_since: SINCE });
    expect(l).not.toContain("GPS fix");
    expect(l).not.toContain("location as of");
  });
});
