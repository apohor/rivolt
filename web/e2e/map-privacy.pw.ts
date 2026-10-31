import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// The drive map is MapLibre on the app's own tiles, glyphs and terrain: no request leaves the origin, and it runs under the
// server's real CSP (script-src/worker-src/connect-src 'self', no unsafe-eval, no blob: workers). The same spec runs in WebKit
// (the "iphone" in the titles selects it), the engine behind iPhone Safari.
//
// Synthetic fixtures only: a made-up straight line near (10, 10), nowhere real.

const CSP =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https://rivian.com https://*.rivian.com; " +
  "font-src 'self' data:; connect-src 'self'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'";

const MIN = 60_000;
const START = e2eNow() - 3 * 3_600_000;

async function setup(page: Page) {
  await stubApi(page);
  const drive = {
    ID: "drive-1", VehicleID: VEHICLE_ID,
    StartedAt: new Date(START).toISOString(), EndedAt: new Date(START + 30 * MIN).toISOString(),
    StartSoCPct: 80, EndSoCPct: 70, StartOdometerMi: 5000, EndOdometerMi: 5020, DistanceMi: 20,
    StartLat: 10, StartLon: 10, EndLat: 10.2, EndLon: 10.2,
    MaxSpeedMph: 70, AvgSpeedMph: 40, EnergyUsedKWh: 8, Source: "live",
  };
  const samples = Array.from({ length: 121 }, (_, i) => ({
    VehicleID: VEHICLE_ID, At: new Date(START + i * 15_000).toISOString(),
    BatteryLevelPct: 80 - i / 12, RangeMi: 250, OdometerMi: 5000 + i / 6,
    Lat: 10 + i * 0.0016, Lon: 10 + i * 0.0016, SpeedMph: 20 + (i % 50),
    ShiftState: "D", ChargingState: "", ChargerPowerKW: 0, ChargeLimitPct: 80,
    InsideTempC: 21, OutsideTempC: 11, DriveNumber: 1, ChargeNumber: 0, Source: "live",
  }));
  await page.route("**/api/drives?**", (r) => r.fulfill({ json: [drive] }));
  await page.route("**/api/samples**", (r) => r.fulfill({ json: samples }));
}

test("iphone: drive map makes no third-party request and trips no CSP rule", async ({ page }) => {
  await setup(page);
  // Serve the page under the production CSP so a blocked worker, eval or foreign fetch is caught here.
  await page.route(
    (u) => u.pathname.startsWith("/drives/"),
    async (route) => {
      if (route.request().resourceType() !== "document") return route.fallback();
      const res = await route.fetch();
      return route.fulfill({ response: res, headers: { ...res.headers(), "content-security-policy": CSP } });
    },
  );
  await page.addInitScript(() => {
    (window as unknown as { __csp: string[] }).__csp = [];
    document.addEventListener("securitypolicyviolation", (e) =>
      (window as unknown as { __csp: string[] }).__csp.push(`${e.violatedDirective} ${e.blockedURI}`),
    );
  });
  const origin = "127.0.0.1";
  const foreign: string[] = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.protocol === "data:" || u.protocol === "blob:") return;
    if (u.hostname !== origin && u.hostname !== "localhost") foreign.push(r.url());
  });

  await page.goto("/drives/drive-1");
  await expect(page.locator(".rivolt-flavor-control").first()).toBeVisible();
  await expect(page.locator(".maplibregl-canvas").first()).toBeVisible();
  // Let the style and first tiles settle.
  await page.waitForTimeout(1500);

  expect(foreign).toEqual([]);
  expect(await page.evaluate(() => (window as unknown as { __csp: string[] }).__csp)).toEqual([]);
});

test("iphone: the terrain credit shows while the hillshade is on", async ({ page }) => {
  await setup(page);
  await page.goto("/drives/drive-1");
  const attr = page.locator(".maplibregl-ctrl-attrib").first();
  await expect(attr).toBeVisible();
  // The basemap's own credit comes with its tiles (the harness serves none); the style's unit test pins it.
  await expect(attr).toContainText("USGS 3DEP");
});
