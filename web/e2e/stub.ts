// Contract-shaped, obviously fake API stubs for the e2e harness. No
// real coordinates, addresses or owner figures: the "home" is a point
// in an empty field and every name is generic.

import type { Page, Route } from "@playwright/test";
import { ledgerMonthFixture, ledgerSummaryFixture, currentMonth } from "../src/lib/ledger";
import { e2eNow, pinClock } from "./clock";

export const VEHICLE_ID = "veh-sample-1";
const HOME = { latitude: 40.0, longitude: -100.0 };

export type NowScenario = "parked" | "charging" | "driving" | "asleep" | "offline" | "stale";

function iso(msAgo: number): string {
  return new Date(e2eNow() - msAgo).toISOString();
}

export function vehicleState(s: NowScenario): Record<string, unknown> {
  const base: Record<string, unknown> = {
    at: iso(8_000),
    vehicle_id: VEHICLE_ID,
    battery_level_pct: 64,
    distance_to_empty: 300,
    odometer_km: 20_000,
    gear: "P",
    charger_state: "",
    charger_status: "chrgr_sts_not_connected",
    charger_power_kw: 0,
    charge_target_pct: 80,
    // Home-ish by default so "Parked at home" renders.
    latitude: HOME.latitude,
    longitude: HOME.longitude,
    speed_kph: 0,
    power_state: "ready",
    tire_pressure_fl_bar: 2.9,
    tire_pressure_fr_bar: 2.9,
    tire_pressure_rl_bar: 2.8,
    tire_pressure_rr_bar: 2.9,
    ota_current_version: "2026.00.0",
    ota_available_version: "",
  };
  switch (s) {
    case "charging":
      return {
        ...base,
        battery_level_pct: 52,
        charger_state: "charging_active",
        charger_status: "chrgr_sts_connected_charging",
        charger_power_kw: 11,
        charge_target_pct: 80,
      };
    case "driving":
      return { ...base, gear: "D", speed_kph: 88, battery_level_pct: 58, latitude: 40.2, longitude: -100.3 };
    case "asleep":
      return { ...base, power_state: "sleep", at: iso(4 * 60_000), battery_level_pct: 71 };
    case "stale":
      // A parked truck whose last frame is 6 min old (e.g. a frozen replay).
      return { ...base, at: iso(6 * 60_000) };
    case "offline":
      return { ...base, at: iso(3 * 3_600_000), battery_level_pct: 47, latitude: 40.4, longitude: -100.1 };
    default:
      return base;
  }
}

const drive = (n: number, daysAgo: number, mi: number, o: Record<string, unknown> = {}) => ({
  ID: `drive-${n}`,
  VehicleID: VEHICLE_ID,
  StartedAt: iso(daysAgo * 86_400_000 + 3_600_000),
  EndedAt: iso(daysAgo * 86_400_000 + 3_000_000),
  StartSoCPct: 70,
  EndSoCPct: 66,
  StartOdometerMi: 1000 + n * 10,
  EndOdometerMi: 1000 + n * 10 + mi,
  DistanceMi: mi,
  StartLat: HOME.latitude,
  StartLon: HOME.longitude,
  EndLat: 40.3,
  EndLon: -100.2,
  MaxSpeedMph: 60,
  AvgSpeedMph: 35,
  EnergyUsedKWh: mi * 0.4,
  Source: "live",
  estimated_cost: mi * 0.04,
  estimated_currency: "USD",
  ...o,
});

const charge = (n: number, daysAgo: number, o: Record<string, unknown> = {}) => ({
  ID: `charge-${n}`,
  VehicleID: VEHICLE_ID,
  StartedAt: iso(daysAgo * 86_400_000 + 7_200_000),
  EndedAt: iso(daysAgo * 86_400_000),
  StartSoCPct: 30,
  EndSoCPct: 80,
  EnergyAddedKWh: 40,
  MilesAdded: 120,
  MaxPowerKW: 11,
  AvgPowerKW: 10,
  FinalState: "charging_complete",
  Lat: HOME.latitude,
  Lon: HOME.longitude,
  Source: "live",
  Cost: 4.4,
  Currency: "USD",
  PricePerKWh: 0.11,
  CostSource: "home_rate",
  ...o,
});

// Sample preset trips served by GET /api/demo/trips (synthetic, not real roads).
export const DEMO_PRESETS = [
  {
    id: "la-vegas",
    name: "LA to Las Vegas",
    inputs: {
      origin: { lat: 34.05, lon: -118.24, label: "Los Angeles" },
      destination: { lat: 36.17, lon: -115.14, label: "Las Vegas" },
      extra_stops: [],
      starting_soc: "90",
      target_soc: "20",
    },
    summary: { distance_mi: 270, duration_min: 245, charge_stops: 1, arrival_soc_pct: 24 },
  },
  {
    id: "la-san-diego",
    name: "LA to San Diego",
    inputs: {
      origin: { lat: 34.05, lon: -118.24, label: "Los Angeles" },
      destination: { lat: 32.72, lon: -117.16, label: "San Diego" },
      extra_stops: [],
      starting_soc: "80",
      target_soc: "20",
    },
    summary: { distance_mi: 120, duration_min: 120, charge_stops: 0, arrival_soc_pct: 52 },
  },
];

export const DEMO_PLAN = {
  Status: "OK",
  ChargeStationsAvailable: true,
  SoCBelowLimit: false,
  Routes: [
    {
      DestinationReached: true,
      TotalChargingDurationSec: 1500,
      TotalDriveDurationSec: 13200,
      TotalTripDurationSec: 14700,
      TotalDriveDistanceMeters: 434500,
      ArrivalSoC: 24,
      ArrivalReachableMeters: 90000,
      EnergyConsumptionKWh: 118,
      BatteryEmptyToDestMeters: 0,
      BatteryEmptyLat: 0,
      BatteryEmptyLon: 0,
      RouteResponseRaw: null,
      Waypoints: [
        { WaypointType: "origin", EntityID: "", Name: "Los Angeles", Latitude: 34.05, Longitude: -118.24, MaxPowerKW: 0, ChargeDurationSec: 0, ArrivalSoC: 90, DepartureSoC: 90, ArrivalReachableMeters: 0, DepartureReachableMeters: 0, AdapterRequired: false, DistanceFromOriginMeters: 0 },
        { WaypointType: "charger", EntityID: "demo-1", Name: "Sample Fast Charge, Barstow", Latitude: 34.9, Longitude: -117.02, MaxPowerKW: 200, ChargeDurationSec: 1500, ArrivalSoC: 38, DepartureSoC: 65, ArrivalReachableMeters: 0, DepartureReachableMeters: 0, AdapterRequired: false, DistanceFromOriginMeters: 215000 },
        { WaypointType: "destination", EntityID: "", Name: "Las Vegas", Latitude: 36.17, Longitude: -115.14, MaxPowerKW: 0, ChargeDurationSec: 0, ArrivalSoC: 24, DepartureSoC: 24, ArrivalReachableMeters: 0, DepartureReachableMeters: 0, AdapterRequired: false, DistanceFromOriginMeters: 434500 },
      ],
    },
  ],
  costs: [
    { currency: "USD", dcfc_spend: 14.4, dcfc_spend_user_member: 11.2, dcfc_spend_all_member: 11.2, home_equivalent: 13, gas_equivalent: 52, gas_price_per_gallon: 4.5, gas_mpg: 25, dcfc_rate_used: 0.5, dcfc_rate_used_user_member: 0.4, dcfc_rate_used_all_member: 0.4, home_rate_used: 0.11 },
  ],
};

export type RivianState = "ok" | "mfa" | "reauth" | "disconnected" | "backend-down" | "paused";

export type DrainMode = "absent" | "ok" | "insufficient";

export type StubOptions = {
  // No basemap archive configured (tiles.url empty).
  noTiles?: boolean;
  // Real basemap for design screenshots: an absolute pmtiles URL (e.g. preview's).
  tilesUrl?: string;
  // features.signup_enabled=false (RIVOLT_SIGNUP_DISABLED).
  signupDisabled?: boolean;
  // GET /api/vehicles/{id}/drain: sufficient, thin data, or 404.
  drain?: DrainMode;
  rivian?: RivianState;
  // Serve GET /api/vehicles/{id}/now (replay connection).
  nowEndpoint?: boolean;
  // Serve GET /api/demo (public sample truck).
  demo?: boolean;
  // Standalone demo deployment: features.demo_only + app_url in /api/config.
  demoOnly?: boolean;
  scenario?: NowScenario;
  signedIn?: boolean;
  landing?: boolean;
  admin?: boolean;
  // The server-side new-look flag on /api/ledger/enabled (no localStorage override).
  uiNext?: boolean;
};

export async function stubApi(page: Page, opts: StubOptions = {}) {
  const { scenario = "parked", signedIn = true, landing = false, admin = true, noTiles = false, tilesUrl = "", signupDisabled = false, rivian = "ok", nowEndpoint = false, demo = false, demoOnly = false, drain = "absent", uiNext = false } = opts;
  const month = currentMonth(new Date(e2eNow()));
  const json = (route: Route, body: unknown, status = 200) =>
    route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  // STUB_NOW pins the page's clock to the fixtures' now (no-op otherwise).
  await pinClock(page);

  // No service worker in the harness: once it controls the page, requests the
  // page makes go through it and WebKit does not hand them to page.route, so
  // the stubbed API never answered ("checking…" forever). The app treats the
  // worker as a progressive enhancement, so the specs lose nothing.
  await page.addInitScript(() => {
    try {
      if (navigator.serviceWorker) navigator.serviceWorker.register = () => new Promise(() => {});
    } catch {
      /* no service worker support */
    }
  });
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
    // Absolute remote URLs (the real basemap in design screenshots) pass through.
    if (url.hostname !== "localhost" && url.hostname !== "127.0.0.1") {
      // preview sends duplicate CORS headers, so fetch it here and re-serve with one.
      const cors = { "access-control-allow-origin": "*", "access-control-allow-headers": "range, if-match, if-none-match", "access-control-expose-headers": "content-range, content-length, etag" };
      if (route.request().method() === "OPTIONS") return route.fulfill({ status: 204, headers: cors });
      // The pass-through outlives a fast test: once the test (and its context) has ended,
      // route.fetch/fulfill reject with "Test ended" and, unhandled, fail the shard after all
      // specs passed. Nobody is waiting for the answer by then, so swallow that.
      try {
        const r = await route.fetch();
        return await route.fulfill({ status: r.status(), headers: { ...r.headers(), ...cors, "access-control-allow-origin": "*" }, body: await r.body() });
      } catch {
        return route.abort().catch(() => {});
      }
    }
    if (p === "/api/auth/me") {
      return signedIn
        ? json(route, { user_id: "u-sample", username: "sample@example.com", role: admin ? "admin" : "user" })
        : json(route, { error: "unauthenticated" }, 401);
    }
    if (p === "/api/config") {
      return json(route, {
        features: { trip_planner_enabled: true, landing_enabled: landing, demo_enabled: demo || demoOnly, signup_enabled: !signupDisabled, ...(demoOnly ? { demo_only: true, app_url: "https://rivolt.dev" } : {}) },
        tiles: { url: noTiles ? "" : tilesUrl || "/api/maps/tiles/us.pmtiles", chargers_url: tilesUrl ? tilesUrl.replace("us.pmtiles", "chargers.pmtiles") : "/api/maps/tiles/chargers.pmtiles" },
        valhalla: { path: "/api/maps/valhalla" },
        impersonation: { disabled: true },
      });
    }
    if (p === "/api/demo") {
      return demo || demoOnly
        ? json(route, { enabled: true, vehicle_id: VEHICLE_ID, home_label: "Sample lot" })
        : json(route, { error: "not found" }, 404);
    }
    if (p === "/api/settings/statement-email") {
      return json(route, { enabled: false, available: true, last_sent_period: null });
    }
    if (p === "/api/statement-email/preview") {
      return json(route, {
        subject: "Your Rivian cost for September: $88.00",
        text: "Hi there,\n\nHere's your September statement from Rivolt.\n\nYou spent $88.00 on charging",
        html: "<p>Here's your September statement.</p>",
        will_send: false,
        reason: "not_opted_in",
      });
    }
    if (p === "/api/ledger/enabled") return json(route, { enabled: true, ...(uiNext ? { ui_theme_next: true } : {}) });
    if (p === "/api/ledger/monthly") return json(route, ledgerMonthFixture(url.searchParams.get("month") || month, new Date(e2eNow())));
    if (p === "/api/ledger/summary") return json(route, ledgerSummaryFixture(12, new Date(e2eNow())));
    if (p === "/api/health") {
      return rivian === "backend-down"
        ? json(route, { error: "down" }, 503)
        : json(route, { ok: true, version: "test", time: iso(0) });
    }
    if (p === "/api/settings/rivian/") {
      return json(route, {
        enabled: true,
        authenticated: rivian === "ok" || rivian === "reauth" || rivian === "mfa" || rivian === "paused",
        mfa_pending: rivian === "mfa",
        needs_reauth: rivian === "reauth",
        paused: rivian === "paused",
        paused_reason: rivian === "paused" ? "operator_pause" : undefined,
      });
    }
    if (p === "/api/vehicles") {
      return json(route, [{ id: VEHICLE_ID, vin: "SAMPLE0000000001", name: "Sample R1S", model: "R1S", pack_kwh: 135 }]);
    }
    if (p === `/api/vehicles/${VEHICLE_ID}/now`) {
      if (!nowEndpoint) return json(route, { error: "not stubbed" }, 404);
      const st = scenario === "charging" ? "charging" : scenario === "driving" ? "driving" : scenario === "asleep" ? "asleep" : "parked";
      return json(route, {
        version: 1, at: iso(8_000), location_fix_at: iso(3 * 3_600_000), connection: "replay", state: st,
        state_since: iso(2 * 3_600_000), soc_at_state_start: 68, tire_pressure_min_bar: 2.8, failed_sleeps_6h: 0,
        charge: scenario === "charging" ? { started_at: iso(3_600_000), soc_start: 40, peak_kw: 11 } : null,
      });
    }
    if (p === `/api/vehicles/${VEHICLE_ID}/drain`) {
      if (drain === "absent") return json(route, { error: "not stubbed" }, 404);
      const ok = drain === "ok";
      const w = (days: number, rate: number) => ({
        drain_pct_per_day: ok ? rate : null, drain_kwh_per_day: ok ? +(rate * 1.35).toFixed(1) : null,
        idle_awake_hours_per_day: ok ? 0.9 : null, sleeps: ok ? days * 3 : 1, quick_rewakes: ok ? Math.round(days / 2) : 0,
        median_sleep_seconds: ok ? 16_000 : null, parked_hours: ok ? days * 20 : 9,
        power_state_observed_hours: ok ? days * 22 : 9, days_in_window: days, days_with_data: ok ? days - 1 : 1,
        days_excluded: { insufficient: ok ? 1 : days - 1 }, sufficient: ok,
      });
      const daily = Array.from({ length: 30 }, (_, i) => {
        const excluded = !ok ? (i < 28 ? "insufficient" : null) : i % 9 === 4 ? "insufficient" : null;
        return {
          day: new Date(e2eNow() - (29 - i) * 86_400_000).toISOString().slice(0, 10),
          drain_pct: excluded || !ok ? null : 0.6 + ((i * 7) % 10) / 10,
          idle_awake_hours: ok ? 0.9 : null, parked_hours: ok ? 20 : 0.3, sleeps: 3, quick_rewakes: 0, excluded,
        };
      });
      return json(route, { version: 1, generated_at: iso(0), tz: "UTC", pack_kwh: 135, windows: { "7d": w(7, 0.9), "30d": w(30, 1.1) }, daily });
    }
    if (p === `/api/state/${VEHICLE_ID}`) return json(route, vehicleState(scenario));
    if (p === `/api/live-drive/${VEHICLE_ID}`) {
      if (scenario !== "driving") return route.fulfill({ status: 204 });
      return json(route, {
        vehicle_id: VEHICLE_ID, number: 1, started_at: iso(600_000), ended_at: iso(0), elapsed_sec: 600,
        start_soc_pct: 60, end_soc_pct: 58, soc_used_pct: 2, start_odometer_mi: 12_400, end_odometer_mi: 12_410,
        distance_mi: 10, max_speed_mph: 62, avg_speed_mph: 40, energy_used_kwh: 2.7, mi_per_kwh: 3.7, pack_kwh: 135,
      });
    }
    if (p === "/api/settings/home-location") return json(route, { set: true, ...HOME, label: "Home" });
    if (p === "/api/settings/charging/") {
      return json(route, { home_price_per_kwh: 0.11, home_currency: "USD", gas_price_per_gallon: 4, comparison_mpg: 20 });
    }
    if (p === "/api/settings/charging/networks") return json(route, []);
    if (p === "/api/sleep-activity") {
      const days = Array.from({ length: 14 }, (_, i) => ({
        day: new Date(e2eNow() - (13 - i) * 86_400_000).toISOString().slice(0, 10) + "T00:00:00Z",
        asleep_h: 20, idle_awake_h: 0.5 + (i % 4) * 0.3, sleeps: 3, failed_sleeps: i === 13 ? 4 : 0,
      }));
      return json(route, days);
    }
    if (p === "/api/drives") return json(route, [
        drive(1, 0.1, 12),
        // Two adjacent short errands that fold into one row; the second
        // doesn't return to its origin, so round-trip merging stays out.
        drive(2, 0.0007, 1.1, { StartedAt: iso(3_600_000), EndedAt: iso(3_300_000) }),
        drive(3, 0.0004, 0.9, {
          StartedAt: iso(2_700_000), EndedAt: iso(2_400_000),
          StartLat: 40.3, StartLon: -100.2, EndLat: 40.5, EndLon: -100.4,
        }),
        drive(4, 1, 34),
      ]);
    if (p === "/api/charges") {
      return json(route, [
        charge(1, 0.5),
        charge(2, 2, { MaxPowerKW: 150, Cost: 18.2, CostSource: "network_estimate", PricePerKWh: 0.4, Lat: 40.9, Lon: -100.9 }),
        charge(3, 4, { Cost: 0, CostSource: "", PricePerKWh: 0, Lat: 41.2, Lon: -100.5 }),
      ]);
    }
    if (p === "/api/charges/clusters") return json(route, []);
    if (p === "/api/samples") {
      // A believable window of samples (every 30 s) so the drive and
      // charge charts render with real axes.
      const since = Date.parse(url.searchParams.get("since") || "") || e2eNow() - 3_600_000;
      const until = Date.parse(url.searchParams.get("until") || "") || since + 3_600_000;
      const out: Record<string, unknown>[] = [];
      for (let t = since, i = 0; t <= until && i < 400; t += 30_000, i++) {
        const k = i / 60;
        out.push({
          VehicleID: VEHICLE_ID, At: new Date(t).toISOString(), BatteryLevelPct: 70 - i * 0.08,
          RangeMi: 220 - i * 0.2, OdometerMi: 1000 + i * 0.4, Lat: 40 + k * 0.01, Lon: -100 + k * 0.01,
          SpeedMph: i > 5 && i < 50 ? 45 + 15 * Math.sin(i / 4) : 0, ShiftState: i > 5 && i < 50 ? "D" : "P",
          ChargingState: i >= 50 ? "charging_active" : "", ChargerPowerKW: i >= 50 ? 11 : 0, ChargeLimitPct: 80,
          InsideTempC: 20, OutsideTempC: 12, DriveNumber: 1, ChargeNumber: 1, Source: "live",
          altitude_m: 200 + 30 * Math.sin(i / 9), pack_temp_avg_c: 25 + i * 0.02,
        });
      }
      return json(route, out);
    }
    if (p === "/api/demo/trips") {
      return demo || demoOnly ? json(route, { trips: DEMO_PRESETS }) : json(route, { error: "not found" }, 404);
    }
    const dp = p.match(/^\/api\/demo\/trips\/([a-z0-9-]+)\/plan$/);
    if (dp) {
      return demo && DEMO_PRESETS.some((x) => x.id === dp[1]) ? json(route, DEMO_PLAN) : json(route, { error: "not found" }, 404);
    }
    if (p === "/api/trips/saved") {
      const inputs = {
        origin: { lat: 40.0, lon: -100.0, label: "Home" },
        destination: { lat: 41.0, lon: -101.0, label: "Sample destination" },
        extra_stops: [],
      };
      return json(
        route,
        ["none", "suggested", "progress", "full", "partial"].map((k, i) => ({
          id: `st-${k}`, name: `Sample trip ${i + 1}`, inputs, created_at: iso(86_400_000), updated_at: iso(86_400_000),
        })),
      );
    }
    const pva = p.match(/^\/api\/trips\/saved\/st-(none|suggested|progress|full|partial)\/actual$/);
    if (pva) {
      const k = pva[1];
      const base = { version: 1, saved_trip_id: `st-${k}` };
      if (k === "none") return json(route, { ...base, status: "none", link: null, candidates: [], comparison: null });
      if (k === "progress") {
        const cand0 = { trip_id: "trip_9", started_at: iso(2 * 3_600_000), ended_at: iso(0), confidence: 0.9, band: "high", signals: {}, in_progress: true };
        return json(route, { ...base, status: "suggested", link: null, candidates: [cand0], comparison: null });
      }
      const metric = (metric: string, unit: string, planned: number, actual: number | null, extra: object = {}) => ({
        metric, unit, planned, actual, delta: actual == null ? null : actual - planned, reasons: [], ...extra,
      });
      const comparison = {
        currency: "USD", plan_from: k === "suggested" ? "current" : "snapshot",
        leg: { started_at: iso(3 * 86_400_000), arrived_at: iso(3 * 86_400_000 - 40_000_000), miles: 612 },
        metrics: [
          k === "partial"
            ? metric("cost", "USD", 52.1, null, { actual_complete: false, actual_known: 21.12, unpriced_sessions: 2 })
            : metric("cost", "USD", 52.1, 48.22, {
                actual_complete: true, actual_known: 48.22, unpriced_sessions: 0,
                actual_sources: { rivian: 30.1, network_estimate: 18.12 },
                reasons: [{ code: "rate_differed", amount: -3.88, detail: "Sample network billed $0.48/kWh vs $0.56 planned" }],
              }),
          metric("energy_kwh", "kWh", 150, 146),
          metric("dcfc_stops", "count", 3, 4, { reasons: [{ code: "extra_stop", amount: 9.5, detail: "One extra stop after a detour" }] }),
          metric("arrival_soc", "pct", 22, 18),
          metric("duration", "min", 640, 700, { reasons: [{ code: "extra_stop", amount: null, detail: "The extra stop added about an hour" }] }),
        ],
        stops: [
          { match: "same_station", planned: { name: "Sample stop A" }, actual: { network: "Sample network" } },
          { match: "extra", planned: null, actual: { network: "Sample network" } },
        ],
      };
      const cand = { trip_id: "trip_1", started_at: iso(3 * 86_400_000), ended_at: iso(3 * 86_400_000 - 40_000_000), confidence: 0.91, band: "high", signals: {} };
      if (k === "suggested") return json(route, { ...base, status: "suggested", link: null, candidates: [cand], comparison });
      return json(route, {
        ...base, status: "linked", candidates: [cand], comparison,
        link: { trip_id: "trip_1", confidence: 0.91, band: "high", linked_at: iso(86_400_000) },
      });
    }
    if (p === `/api/vehicles/${VEHICLE_ID}/pack-health`) {
      const samples = Array.from({ length: 8 }, (_, i) => ({
        vehicle_id: VEHICLE_ID, charge_id: `c${i}`, at: iso((8 - i) * 5 * 86_400_000),
        pack_kwh_effective: 130 - i * 0.2, soc_delta_pct: 50, energy_delivered_kwh: 65, derate_active: false,
      }));
      return json(route, {
        samples,
        headline: {
          effective_kwh: 128.6, nameplate_kwh: 135, pct_of_nameplate: 95, window: 5, sample_count: 8,
          reported_kwh: 129, documented_kwh: 135, reported_pct_of_documented: 95.5,
        },
      });
    }
    if (p === "/api/admin/idp-delete-failures") {
      return json(route, [
        { id: "f1", username: "former-user@example.com", error: "identity provider returned 503", attempts: 2,
          failed_at: iso(86_400_000), last_tried: iso(3_600_000), account_exists: false },
        { id: "f2", username: "returned-user@example.com", error: "identity provider returned 503", attempts: 1,
          failed_at: iso(86_400_000), last_tried: iso(7_200_000), account_exists: true },
      ]);
    }
    if (p === "/api/admin/users") return json(route, { users: [] });
    // The charger corridor query: signed in it finds none along the stub's routes
    // (the chips and their "no chargers here" note show); the demo never calls it.
    if (p === "/api/maps/chargers-along") return json(route, { chargers: [], count: 0 });
    // Anything else answers 404 like an absent feature.
    return json(route, { error: "not stubbed" }, 404);
  });
}
