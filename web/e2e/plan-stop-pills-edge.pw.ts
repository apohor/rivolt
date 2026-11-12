import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The owner's trip on a phone (El Paso to Big Bend NP, fast chargers, weather and radar on): after the map is panned so that a stop sits near the left edge,
// the stop's pill ("34 min · 23→64%") is placed again inside the map instead of running past the edge. Evidence is the screenshot (the pills are map symbols).
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("El Paso", 31.76, -106.49), destination: pt("Big Bend NP", 29.25, -103.25), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
const wp = (type: string, name: string, lat: number, lon: number, dur: number, arr: number, dep: number, dist: number) => ({
  WaypointType: type, EntityID: "", Name: name, Latitude: lat, Longitude: lon, MaxPowerKW: dur ? 200 : 0, ChargeDurationSec: dur, ArrivalSoC: arr, DepartureSoC: dep,
  ArrivalReachableMeters: 0, DepartureReachableMeters: 0, AdapterRequired: false, DistanceFromOriginMeters: dist,
});
const PLAN = {
  Status: "OK",
  ChargeStationsAvailable: true,
  SoCBelowLimit: false,
  Routes: [{
    DestinationReached: true, TotalChargingDurationSec: 9000, TotalDriveDurationSec: 60000, TotalTripDurationSec: 69000, TotalDriveDistanceMeters: 1800000, ArrivalSoC: 22,
    ArrivalReachableMeters: 90000, EnergyConsumptionKWh: 400, BatteryEmptyToDestMeters: 0, BatteryEmptyLat: 0, BatteryEmptyLon: 0, RouteResponseRaw: null,
    Waypoints: [
      wp("origin", "El Paso", 31.76, -106.49, 0, 90, 90, 0),
      wp("charger", "Sample Charge, Van Horn", 31.04, -104.83, 1500, 23, 64, 190000),
      wp("charger", "Sample Charge, Marfa", 30.31, -104.02, 1500, 23, 64, 290000),
      wp("charger", "Sample Charge, Alpine", 30.36, -103.66, 1500, 23, 64, 330000),
      wp("charger", "Sample Charge, Fort Stockton", 30.89, -102.88, 1500, 23, 64, 420000),
      wp("charger", "Sample Charge, Terlingua", 29.32, -103.61, 1500, 23, 64, 560000),
      wp("destination", "Big Bend NP", 29.25, -103.25, 0, 22, 22, 600000),
    ],
  }],
  costs: [{ currency: "USD", dcfc_spend_low: 93, dcfc_spend_high: 114, priced_stops: 3, unpriced_stops: 2, estimate_basis: "observed", home_equivalent: 80, gas_equivalent: 224, gas_price_per_gallon: 4.5, gas_mpg: 25, home_rate_used: 0.11, breakdown: [] }],
  weather_adjustment: {
    adjusted_arrival_soc: [34, 30, 28, 26, 24, 22], final_arrival_soc: 22, below_target: false, target_arrival_soc: 10, profile_multiplier: 1,
    legs: [1, 2, 3, 4, 5, 6].map((i) => ({ multiplier: 1.05, temp_c: 14 + i, air_temp_c: 14 + i, headwind_kph: 12, precip_mm: 0 })),
  },
};

test("@webkit phone: stop pills stay inside the map after a pan · El Paso to Big Bend NP", async ({ browser }, info) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.route("**/api/config", (r) =>
    r.fulfill(j({
      features: { trip_planner_enabled: true, landing_enabled: false, demo_enabled: false, signup_enabled: true },
      tiles: { url: "/api/maps/tiles/us.pmtiles", chargers_url: "/api/maps/tiles/chargers.pmtiles", glyphs_url: "/api/maps/glyphs/{fontstack}/{range}.pbf", dem_url: "/api/maps/dem/{z}/{x}/{y}.png", dem_minzoom: 3, dem_maxzoom: 10, radar_url: "/api/maps/radar/{t}/{z}/{x}/{y}.png", radar_frame_url: "/api/maps/radar/frame", radar_minzoom: 3, radar_maxzoom: 9 },
      valhalla: { path: "/api/maps/valhalla" },
      impersonation: { disabled: true },
    })),
  );
  await page.route("**/api/maps/radar/frame", (r) => r.fulfill(j({ frame_unix: Math.floor(Date.now() / 1000) - 300 })));
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(PLAN)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await page.locator("[data-leg-badge]").first().waitFor();
  await expect(page.getByRole("button", { name: "Radar", exact: true })).toHaveAttribute("aria-pressed", "true"); // on by default
  const m = await page.locator("[data-plan-area='map']").boundingBox();
  if (!m) throw new Error("no map");
  // Pan the map left so stops sit at its left edge.
  const y = m.y + m.height * 0.6;
  await page.mouse.move(m.x + m.width * 0.4, y);
  await page.mouse.down();
  await page.mouse.move(m.x + m.width * 0.4 - 190, y, { steps: 8 });
  await page.mouse.up();
  await page.waitForTimeout(3000);
  await page.screenshot({ path: info.outputPath("plan-stop-pills-edge.png") });
  // The leg badges (DOM) stay inside the map after the pan; the stop pills are map symbols, so the screenshot is the evidence for them.
  const after = await page.locator("[data-plan-area='map']").boundingBox();
  if (!after) throw new Error("no map");
  const badges = page.locator("[data-leg-badge]");
  for (let i = 0; i < (await badges.count()); i++) {
    const b = await badges.nth(i).boundingBox();
    if (!b) continue;
    expect(b.x).toBeGreaterThanOrEqual(after.x);
    expect(b.x + b.width).toBeLessThanOrEqual(after.x + after.width);
  }
});
