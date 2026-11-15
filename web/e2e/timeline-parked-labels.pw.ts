import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// A parked stretch is squeezed to a few pixels on the time axis, so the round clock times inside it used to print on top of each other.
// One made-up 20-minute drive with a ten-minute stop in the middle; no two axis labels may overlap, at a phone and a desktop width.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const MIN = 60_000;
const START = e2eNow() - 3 * 3_600_000;

for (const [vpName, vp] of [
  ["393", { width: 393, height: 852 }],
  ["1280", { width: 1280, height: 900 }],
] as const) {
  test(`timeline axis labels stay apart at a parked stop · ${vpName}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page, {});
    const drive = {
      ID: "drive-1", VehicleID: VEHICLE_ID,
      StartedAt: new Date(START).toISOString(), EndedAt: new Date(START + 20 * MIN).toISOString(),
      StartSoCPct: 80, EndSoCPct: 76, StartOdometerMi: 5000, EndOdometerMi: 5006, DistanceMi: 6,
      StartLat: 10, StartLon: 10, EndLat: 10.06, EndLon: 10.06,
      MaxSpeedMph: 45, AvgSpeedMph: 25, EnergyUsedKWh: 2, Source: "live",
    };
    // Samples every 15 s while driving (minutes 0-3 and 13-20); the car sits still in between, with no samples.
    const samples = Array.from({ length: 81 }, (_, i) => i)
      .filter((i) => i <= 12 || i >= 52)
      .map((i) => {
        const moved = i <= 12 ? i : i - 40;
        return {
          VehicleID: VEHICLE_ID, At: new Date(START + i * 15_000).toISOString(),
          BatteryLevelPct: 80 - i * 0.07, RangeMi: 250, OdometerMi: 5000 + moved / 10,
          Lat: 10 + moved * 0.0015, Lon: 10 + moved * 0.0015, SpeedMph: 20 + 20 * Math.abs(Math.sin(i / 5)),
          ShiftState: "D", ChargingState: "", ChargerPowerKW: 0, ChargeLimitPct: 80,
          InsideTempC: 21, OutsideTempC: 11, DriveNumber: 1, ChargeNumber: 0, Source: "live",
        };
      });
    await page.route("**/api/drives?**", (r) => r.fulfill({ json: [drive] }));
    await page.route("**/api/samples**", (r) => r.fulfill({ json: samples }));
    await page.goto("/drives/drive-1");
    const svg = page.locator('svg[aria-label^="Drive timeline"]');
    await expect(svg).toBeVisible();
    await svg.scrollIntoViewIfNeeded();
    const labels = svg.locator("text").filter({ hasText: /^\d{1,2}:\d{2}/ });
    await expect(labels.first()).toBeVisible();
    const boxes = (await labels.evaluateAll((els) => els.map((e) => e.getBoundingClientRect()).map((r) => ({ l: r.left, r: r.right, t: r.top }))))
      .sort((a, b) => a.l - b.l);
    expect(boxes.length).toBeGreaterThan(1);
    for (let i = 1; i < boxes.length; i++) expect(boxes[i].l, `label ${i} starts after label ${i - 1} ends`).toBeGreaterThanOrEqual(boxes[i - 1].r);
    await svg.evaluate((e) => e.scrollIntoView({ block: "start" }));
    await svg.screenshot({ path: path.join(SHOTS, `timeline-parked-${vpName}.png`) });
  });
}
