import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// Drive timeline: stacked Speed / Power / Battery panels with a synced
// scrub readout. Uses a realistic fixture (stops, slowdowns, regen, one
// drive-mode change) instead of the stub's sine wave. Screenshots land in
// e2e/__screenshots__ (git-ignored). Coordinates are synthetic.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const MIN = 60_000;
const NOW = e2eNow();
const LONG = { id: "drive-1", start: NOW - 3 * 3_600_000, mins: 75, mi: 62 };
const SHORT = { id: "drive-2", start: NOW - 6 * 3_600_000, mins: 9, mi: 3.4 };

// Speed in mph at fraction f of the long highway drive: city ramp, a stop
// light, highway cruise with a slowdown, a downhill regen stretch, exit.
function longSpeed(f: number): number {
  if (f < 0.04) return f * 25 * 400 / 10; // pull out
  if (f < 0.1) return 28 + 6 * Math.sin(f * 90);
  if (f >= 0.1 && f < 0.115) return Math.max(0, 28 * (1 - (f - 0.1) / 0.015)); // light
  if (f < 0.13) return 0;
  if (f < 0.2) return Math.min(70, (f - 0.13) * 1000);
  if (f < 0.5) return 70 + 2 * Math.sin(f * 60);
  if (f < 0.54) return 70 - 45 * Math.sin(((f - 0.5) / 0.04) * Math.PI); // traffic: braking
  if (f < 0.88) return 68 + 3 * Math.sin(f * 40);
  if (f < 0.92) return 68 - ((f - 0.88) / 0.04) * 40; // exit ramp
  if (f < 0.95) return 28;
  return Math.max(0, 28 - ((f - 0.95) / 0.03) * 28);
}
function shortSpeed(f: number): number {
  if (f < 0.15) return f * 200;
  if (f < 0.45) return 30 + 5 * Math.sin(f * 30);
  if (f < 0.55) return Math.max(0, 30 * (1 - (f - 0.45) / 0.1));
  if (f < 0.65) return 0;
  if (f < 0.9) return Math.min(35, (f - 0.65) * 160);
  return Math.max(0, 35 * (1 - (f - 0.9) / 0.1));
}

function samplesFor(d: typeof LONG, speed: (f: number) => number, startSoc: number, endSoc: number) {
  const out: Record<string, unknown>[] = [];
  const n = Math.round((d.mins * MIN) / 15_000);
  for (let i = 0; i <= n; i++) {
    const f = i / n;
    const v = speed(f);
    const moving = v > 1.5;
    out.push({
      VehicleID: VEHICLE_ID,
      At: new Date(d.start + f * d.mins * MIN).toISOString(),
      BatteryLevelPct: startSoc - (startSoc - endSoc) * f,
      RangeMi: 250 - 200 * f,
      OdometerMi: 5000 + d.mi * f,
      Lat: 40 + f * 0.2,
      Lon: -100 + f * 0.2,
      SpeedMph: v,
      ShiftState: moving || f < 0.995 ? "D" : "P",
      // Sport for the middle third of the long drive: one mode change.
      drive_mode: d === LONG && f > 0.45 && f < 0.7 ? "sport" : "everyday",
      ChargingState: "",
      ChargerPowerKW: 0,
      ChargeLimitPct: 80,
      InsideTempC: 21,
      OutsideTempC: 11,
      DriveNumber: 1,
      ChargeNumber: 0,
      Source: "live",
      // Realistic relief: city +-30-80 ft, gentle on the highway.
      altitude_m: d === LONG ? 180 + 14 * Math.sin(f * 3) : 180 + 9 * Math.sin(f * 6),
      pack_temp_avg_c: 24 + f * 6,
    });
  }
  return out;
}

async function setup(page: Page) {
  await stubApi(page);
  const mk = (d: typeof LONG, soc0: number, soc1: number) => ({
    ID: d.id, VehicleID: VEHICLE_ID,
    StartedAt: new Date(d.start).toISOString(), EndedAt: new Date(d.start + d.mins * MIN).toISOString(),
    StartSoCPct: soc0, EndSoCPct: soc1, StartOdometerMi: 5000, EndOdometerMi: 5000 + d.mi, DistanceMi: d.mi,
    StartLat: 40, StartLon: -100, EndLat: 40.2, EndLon: -99.8,
    MaxSpeedMph: 73, AvgSpeedMph: Math.round(d.mi / (d.mins / 60)), EnergyUsedKWh: d.mi * 0.4,
    Source: "live", estimated_cost: d.mi * 0.04, estimated_currency: "USD",
  });
  const drives = [mk(SHORT, 58, 56), mk(LONG, 82, 54)].reverse();
  await page.route("**/api/drives?**", (r) => r.fulfill({ json: drives }));
  await page.route("**/api/samples**", (r) => {
    const since = Date.parse(new URL(r.request().url()).searchParams.get("since") || "");
    const d = Math.abs(since - SHORT.start) < Math.abs(since - LONG.start) ? SHORT : LONG;
    return r.fulfill({ json: d === LONG ? samplesFor(LONG, longSpeed, 82, 54) : samplesFor(SHORT, shortSpeed, 58, 56) });
  });
}

for (const [vpName, vp] of [
  ["desktop", { width: 1280, height: 900 }],
  ["iphone", { width: 390, height: 844 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    for (const [dName, d] of [["long", LONG], ["short", SHORT]] as const) {
      test(`timeline · ${dName} drive · ${vpName} · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: vp, deviceScaleFactor: 2 });
        const page = await ctx.newPage();
        await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
        await setup(page);
        await page.goto(`/drives/${d.id}`);
        const scrub = page.getByTestId("timeline-scrub");
        await expect(scrub).toBeVisible();
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        expect(overflow).toBeLessThanOrEqual(1);
        // Scrubbing updates the shared readout.
        const readout = page.getByTestId("timeline-readout");
        await expect(readout).toContainText("Whole drive");
        await scrub.scrollIntoViewIfNeeded();
        const box = (await scrub.boundingBox())!;
        await page.mouse.move(box.x + box.width * 0.4, box.y + box.height / 2);
        await expect(readout).toContainText("mph");
        await expect(readout).not.toContainText("Whole drive");
        await expect(page.getByRole("img", { name: /Drive timeline/ })).toBeVisible();
        await page.getByRole("img", { name: /Drive timeline/ }).scrollIntoViewIfNeeded();
        await page.addStyleTag({ content: "nav, header, [class*=fixed], [class*=sticky] { position: static !important; }" });
        await page.screenshot({ path: path.join(SHOTS, `timeline-${dName}-${vpName}-${theme}.png`), fullPage: true });
        await ctx.close();
      });
    }
  }
}

test("a vertical swipe starting on the chart scrolls the page and leaves no cursor", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  const page = await ctx.newPage();
  await setup(page);
  await page.goto("/drives/drive-1");
  const scrub = page.getByTestId("timeline-scrub");
  await scrub.scrollIntoViewIfNeeded();
  const box = (await scrub.boundingBox())!;
  const x = box.x + box.width / 2;
  const y0 = Math.min(box.y + box.height - 10, 700);
  const before = await page.evaluate(() => window.scrollY);
  const cdp = await ctx.newCDPSession(page);
  await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x, y: y0 }] });
  for (let i = 1; i <= 8; i++) {
    await cdp.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [{ x, y: y0 - i * 25 }] });
  }
  await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
  await page.waitForTimeout(400);
  expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(before);
  await expect(page.getByTestId("timeline-readout")).toContainText("Whole drive");
  await ctx.close();
});
