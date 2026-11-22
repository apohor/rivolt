import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";
import { DRAWN_MS, drawnMap } from "./drawn";

// A tunnel-style dropout in the track is drawn solid and faded in the route's own colour, not dashed grey.
// One made-up drive with the samples from 50 to 70 missing (five minutes, about a mile), shot.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const MIN = 60_000;
const START = e2eNow() - 3 * 3_600_000;

async function setup(page: Page) {
  await stubApi(page);
  const drive = {
    ID: "drive-1", VehicleID: VEHICLE_ID,
    StartedAt: new Date(START).toISOString(), EndedAt: new Date(START + 30 * MIN).toISOString(),
    StartSoCPct: 80, EndSoCPct: 70, StartOdometerMi: 5000, EndOdometerMi: 5020, DistanceMi: 20,
    StartLat: 10, StartLon: 10, EndLat: 10.192, EndLon: 10.192,
    MaxSpeedMph: 78, AvgSpeedMph: 40, EnergyUsedKWh: 8, Source: "live",
  };
  const samples = Array.from({ length: 121 }, (_, i) => i)
    .filter((i) => i < 50 || i >= 70)
    .map((i) => ({
      VehicleID: VEHICLE_ID, At: new Date(START + i * 15_000).toISOString(),
      BatteryLevelPct: 80 - i * 0.083, RangeMi: 250, OdometerMi: 5000 + i / 6,
      Lat: 10 + i * 0.0016, Lon: 10 + i * 0.0016 + 0.01 * Math.sin(i / 14), SpeedMph: 30 + 25 * Math.abs(Math.sin(i / 17)),
      ShiftState: "D", ChargingState: "", ChargerPowerKW: 0, ChargeLimitPct: 80,
      InsideTempC: 21, OutsideTempC: 11, DriveNumber: 1, ChargeNumber: 0, Source: "live",
    }));
  await page.route("**/api/drives?**", (r) => r.fulfill({ json: [drive] }));
  await page.route("**/api/samples**", (r) => r.fulfill({ json: samples }));
}

for (const [vpName, vp] of [
  ["393", { width: 393, height: 852 }],
  ["1280", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["light", "dark"] as const) {
    {
      test(`gap dim · ${vpName} · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
        await setup(page);
        await page.goto("/drives/drive-1");
        await expect(drawnMap(page)).toBeAttached({ timeout: DRAWN_MS });
        await page.waitForTimeout(2500);
        await page.locator("[data-map-drawn]").first().locator("xpath=..").screenshot({ path: path.join(SHOTS, `gap-dim-${vpName}-${theme}.png`) });
        await ctx.close();
      });
    }
  }
}
