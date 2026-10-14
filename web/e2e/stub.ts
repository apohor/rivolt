// Contract-shaped, obviously fake API stubs for the e2e harness. No
// real coordinates, addresses or owner figures: the "home" is a point
// in an empty field and every name is generic.

import type { Page, Route } from "@playwright/test";
import { ledgerMonthFixture, ledgerSummaryFixture, currentMonth } from "../src/lib/ledger";

export const VEHICLE_ID = "veh-sample-1";
const HOME = { latitude: 40.0, longitude: -100.0 };

export type NowScenario = "parked" | "charging" | "driving" | "asleep" | "offline" | "stale";

function iso(msAgo: number): string {
  return new Date(Date.now() - msAgo).toISOString();
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

export type RivianState = "ok" | "mfa" | "reauth" | "disconnected" | "backend-down" | "paused";

export type DrainMode = "absent" | "ok" | "insufficient";

export type StubOptions = {
  // features.signup_enabled=false (RIVOLT_SIGNUP_DISABLED).
  signupDisabled?: boolean;
  // GET /api/vehicles/{id}/drain: sufficient, thin data, or 404.
  drain?: DrainMode;
  rivian?: RivianState;
  // Serve GET /api/vehicles/{id}/now (replay connection).
  nowEndpoint?: boolean;
  // Serve GET /api/demo (public sample truck).
  demo?: boolean;
  scenario?: NowScenario;
  signedIn?: boolean;
  landing?: boolean;
  admin?: boolean;
};

export async function stubApi(page: Page, opts: StubOptions = {}) {
  const { scenario = "parked", signedIn = true, landing = false, admin = true, signupDisabled = false, rivian = "ok", nowEndpoint = false, demo = false, drain = "absent" } = opts;
  const month = currentMonth();
  const json = (route: Route, body: unknown, status = 200) =>
    route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
    if (p === "/api/auth/me") {
      return signedIn
        ? json(route, { user_id: "u-sample", username: "sample@example.com", role: admin ? "admin" : "user" })
        : json(route, { error: "unauthenticated" }, 401);
    }
    if (p === "/api/config") {
      return json(route, {
        features: { trip_planner_enabled: true, landing_enabled: landing, demo_enabled: demo, signup_enabled: !signupDisabled },
        impersonation: { disabled: true },
      });
    }
    if (p === "/api/demo") {
      return demo
        ? json(route, { enabled: true, vehicle_id: VEHICLE_ID, home_label: "Sample lot" })
        : json(route, { error: "not found" }, 404);
    }
    if (p === "/api/ledger/enabled") return json(route, { enabled: true });
    if (p === "/api/ledger/monthly") return json(route, ledgerMonthFixture(url.searchParams.get("month") || month));
    if (p === "/api/ledger/summary") return json(route, ledgerSummaryFixture(12));
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
          day: new Date(Date.now() - (29 - i) * 86_400_000).toISOString().slice(0, 10),
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
        day: new Date(Date.now() - (13 - i) * 86_400_000).toISOString().slice(0, 10) + "T00:00:00Z",
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
    if (p === "/api/trips/saved") {
      const inputs = {
        origin: { lat: 40.0, lon: -100.0, label: "Home" },
        destination: { lat: 41.0, lon: -101.0, label: "Sample destination" },
        extra_stops: [],
      };
      return json(
        route,
        ["none", "suggested", "full", "partial"].map((k, i) => ({
          id: `st-${k}`, name: `Sample trip ${i + 1}`, inputs, created_at: iso(86_400_000), updated_at: iso(86_400_000),
        })),
      );
    }
    const pva = p.match(/^\/api\/trips\/saved\/st-(none|suggested|full|partial)\/actual$/);
    if (pva) {
      const k = pva[1];
      const planned = { currency: "USD", cost: 52.1, kwh: 150, stops: 3, network: "Sample network", arrival_soc: 22, duration_min: 640 };
      const base = { saved_trip_id: `st-${k}`, planned };
      if (k === "none") return json(route, { ...base, status: "none" });
      if (k === "suggested") {
        return json(route, { ...base, status: "suggested", candidate: { trip_id: "trip-1", started_at: iso(3 * 86_400_000), miles: 612 } });
      }
      const actual = {
        cost: k === "full" ? 48.22 : null, cost_source: k === "full" ? "network_estimate" : "unknown",
        kwh: 146, stops: 4, networks: ["Sample network"], arrival_soc: 18, duration_min: 700,
        unpriced_sessions: k === "full" ? 0 : 2, started_at: iso(3 * 86_400_000),
      };
      return json(route, {
        ...base, status: "matched", actual,
        reasons: { stops: "One extra stop after a detour", duration: "The extra stop added about an hour" },
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
    // Anything else answers 404 like an absent feature.
    return json(route, { error: "not stubbed" }, 404);
  });
}
