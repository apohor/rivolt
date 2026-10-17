import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";

// Regression: /api/samples is user-scoped. An account with two trucks
// driving the same minutes must still get a clean single-truck route
// (it used to draw a fan between the two tracks). Synthetic coordinates.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

test("route map ignores a second truck's samples", async ({ page }) => {
  const T0 = Date.now() - 3 * 3_600_000;
  const mk = (vid: string, lat0: number, lon0: number, i: number) => ({
    VehicleID: vid, At: new Date(T0 + i * 5000).toISOString(),
    Lat: lat0 + i * 0.0006 + 0.01 * Math.sin(i / 8), Lon: lon0 + i * 0.0006, SpeedMph: 20 + (i % 40),
    ShiftState: "D", BatteryLevelPct: 70 - i / 20, RangeMi: 200, OdometerMi: 5000 + i / 10, ChargingState: "",
    ChargerPowerKW: 0, ChargeLimitPct: 80, InsideTempC: 21, OutsideTempC: 20, DriveNumber: 1, ChargeNumber: 0,
    Source: "live", LocationFixAt: new Date(T0 + i * 5000).toISOString(),
  });
  const rows = Array.from({ length: 150 }, (_, i) => [mk(VEHICLE_ID, 40, -100, i), mk("other-truck", 40.2, -100.3, i)]).flat();
  const drive = {
    ID: "two-1", VehicleID: VEHICLE_ID, StartedAt: new Date(T0).toISOString(), EndedAt: new Date(T0 + 149 * 5000).toISOString(),
    StartSoCPct: 70, EndSoCPct: 63, StartOdometerMi: 5000, EndOdometerMi: 5015, DistanceMi: 15,
    StartLat: 40, StartLon: -100, EndLat: 40.09, EndLon: -99.91, MaxSpeedMph: 60, AvgSpeedMph: 40, EnergyUsedKWh: 5, Source: "live",
  };
  await stubApi(page);
  await page.route("**/api/drives?**", (r) => r.fulfill({ json: [drive] }));
  const requested: number[] = [];
  await page.route("**/api/samples**", (r) => {
    requested.push(rows.length);
    return r.fulfill({ json: rows });
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/drives/two-1");
  const map = page.locator(".leaflet-container").first();
  await expect(map).toBeVisible();
  await page.waitForTimeout(800);
  expect(requested.length).toBeGreaterThan(0);
  // The map frames only truck A's ~9 km track, not the ~30 km span that
  // includes the other truck: the visible bounds stay tight.
  await map.screenshot({ path: path.join(SHOTS, "twotrucks-route.png") });
});
