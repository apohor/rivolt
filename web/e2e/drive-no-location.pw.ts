import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// A drive whose truck sent no usable live location (the server sets LocationUnavailable): the map area says what was observed, with no
// cause and no fault, instead of an empty map.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

test("a drive with no reported location says so, in plain words", async ({ page }) => {
  await stubApi(page);
  const NOW = e2eNow();
  const iso = (ms: number) => new Date(NOW - ms).toISOString();
  const d = { ID: "drive-nl", VehicleID: "veh-sample-1", StartedAt: iso(7_200_000), EndedAt: iso(3_600_000), StartSoCPct: 70, EndSoCPct: 60, StartOdometerMi: 100, EndOdometerMi: 130, DistanceMi: 30, StartLat: 0, StartLon: 0, EndLat: 0, EndLon: 0, MaxSpeedMph: 60, AvgSpeedMph: 35, EnergyUsedKWh: 10, Source: "live", RoutePolyline: "", LocationUnavailable: true };
  await page.route("**/api/drives?*", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([d]) }));
  await page.route("**/api/drives/drive-nl", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(d) }));
  await page.route("**/api/samples*", (r) => r.fulfill({ status: 200, contentType: "application/json", body: "[]" }));
  await page.goto("/drives/drive-nl");
  await expect(page.locator("[data-no-location]")).toHaveText("The vehicle didn't report its location during this drive.");
  await page.screenshot({ path: path.join(SHOTS, "drive-no-location.png") });
});
