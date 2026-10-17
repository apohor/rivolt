import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";

// The drive map's colour scheme follows Settings → Appearance; the D/L
// buttons are a temporary override that is never persisted.

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const MIN = 60_000;
const START = Date.now() - 3 * 3_600_000;

async function setup(page: Page) {
  await stubApi(page);
  const drive = {
    ID: "drive-1", VehicleID: VEHICLE_ID,
    StartedAt: new Date(START).toISOString(), EndedAt: new Date(START + 30 * MIN).toISOString(),
    StartSoCPct: 80, EndSoCPct: 70, StartOdometerMi: 5000, EndOdometerMi: 5020, DistanceMi: 20,
    StartLat: 40, StartLon: -100, EndLat: 40.2, EndLon: -99.8,
    MaxSpeedMph: 70, AvgSpeedMph: 40, EnergyUsedKWh: 8, Source: "live",
  };
  const samples = Array.from({ length: 121 }, (_, i) => ({
    VehicleID: VEHICLE_ID, At: new Date(START + i * 15_000).toISOString(),
    BatteryLevelPct: 80 - i / 12, RangeMi: 250, OdometerMi: 5000 + i / 6,
    Lat: 40 + i * 0.0016, Lon: -100 + i * 0.0016, SpeedMph: 20 + (i % 50),
    ShiftState: "D", ChargingState: "", ChargerPowerKW: 0, ChargeLimitPct: 80,
    InsideTempC: 21, OutsideTempC: 11, DriveNumber: 1, ChargeNumber: 0, Source: "live",
  }));
  await page.route("**/api/drives?**", (r) => r.fulfill({ json: [drive] }));
  await page.route("**/api/samples**", (r) => r.fulfill({ json: samples }));
}

// The active flavor is the highlighted D/L button.
const active = (page: Page) =>
  page.evaluate(() => {
    const on = [...document.querySelectorAll(".rivolt-flavor-control button")].find(
      (b) => (b as HTMLElement).style.background.includes("38, 38, 38") || (b as HTMLElement).style.background.includes("#262626"),
    );
    return on?.textContent ?? null;
  });

for (const [vpName, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`drive map follows ${theme} · ${vpName}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme === "dark" ? "light" : "dark" });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await setup(page);
      await page.goto("/drives/drive-1");
      const ctl = page.locator(".rivolt-flavor-control").first();
      await expect(ctl).toBeVisible();
      expect(await active(page)).toBe(theme === "dark" ? "D" : "L");
      await ctl.scrollIntoViewIfNeeded();
      await page.locator(".leaflet-container").first().screenshot({ path: path.join(SHOTS, `mapscheme-${vpName}-${theme}.png`) });

      // Override: temporary, not persisted.
      await ctl.locator(`button[title="${theme === "dark" ? "Light" : "Dark"} basemap"]`).click();
      expect(await active(page)).toBe(theme === "dark" ? "L" : "D");
      expect(await page.evaluate(() => Object.keys(localStorage).filter((k) => /flavor|basemap/i.test(k)))).toEqual([]);
      await page.reload();
      await expect(page.locator(".rivolt-flavor-control").first()).toBeVisible();
      expect(await active(page)).toBe(theme === "dark" ? "D" : "L");
      await ctx.close();
    });
  }
}

test("System follows an OS switch while the map is open", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await setup(page);
  await page.goto("/drives/drive-1");
  await expect(page.locator(".rivolt-flavor-control").first()).toBeVisible();
  expect(await active(page)).toBe("D");
  await page.emulateMedia({ colorScheme: "light" });
  await expect.poll(() => active(page)).toBe("L");
  await ctx.close();
});
