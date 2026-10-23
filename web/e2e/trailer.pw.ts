import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// A trailer on a drive: the tag on the list and the drive page, the correction on the drive page, and the "Excludes N drives
// with a trailer" note where an efficiency figure is shown.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ago = (days: number) => new Date(Date.now() - days * 86_400_000).toISOString();
const drive = (id: string, days: number, o: Record<string, unknown> = {}) => ({
  ID: id, VehicleID: VEHICLE_ID, StartedAt: ago(days), EndedAt: ago(days - 0.02), StartSoCPct: 70, EndSoCPct: 60, StartOdometerMi: 100, EndOdometerMi: 130,
  DistanceMi: 30, StartLat: 40, StartLon: -100, EndLat: 40.4, EndLon: -100.6, MaxSpeedMph: 70, AvgSpeedMph: 40, EnergyUsedKWh: 12, Source: "live", RoutePolyline: "", ...o,
});

async function open(page: Page, url: string, drives: ReturnType<typeof drive>[]) {
  await stubApi(page, { scenario: "parked" });
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  await page.route(/\/api\/drives(\?|$)/, (r) => r.fulfill(j(drives)));
  await page.route(/\/api\/drives\/[^/?]+$/, (r) => {
    const id = new URL(r.request().url()).pathname.split("/").pop();
    const d = drives.find((x) => x.ID === id);
    return d ? r.fulfill(j(d)) : r.fulfill({ status: 404, contentType: "application/json", body: "{}" });
  });
  await page.route(/\/api\/samples/, (r) => r.fulfill(j([])));
  await page.addInitScript(() => localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 })));
  await page.goto(url);
}

const DRIVES = [
  drive("plain", 3),
  drive("towing", 2, { Trailer: true, DistanceMi: 40, EnergyUsedKWh: 30 }),
  drive("fixed", 1, { Trailer: true, TrailerOverride: false }),
];

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`the list and the Home tile say a trailer, using the effective value · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, "/activity", DRIVES);
    await expect(page.locator("[data-drive-trailer]")).toHaveCount(1);
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(SHOTS, `trailer-list-${v.n}.png`), fullPage: true });
    await page.goto("/");
    await expect(page.getByText("excludes 1 drive with a trailer")).toBeVisible();
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(SHOTS, `trailer-home-${v.n}.png`), fullPage: true });
    await ctx.close();
  });

  test(`the drive page tags a trailer and corrects it · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, "/drives/towing", DRIVES);
    await expect(page.locator("[data-drive-trailer]")).toContainText("Trailer connected");
    const select = page.locator("[data-trailer-control] select");
    await expect(select).toHaveValue("auto");
    await expect(select.locator("option").first()).toContainText("the truck reported a trailer");
    await expect(page.locator("[data-trailer-control]").locator("xpath=..")).toContainText("bike rack");
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(SHOTS, `trailer-drive-${v.n}.png`), fullPage: true });
    let body: unknown = null;
    await page.route(/\/api\/drives\/towing\/trailer$/, (r) => {
      body = r.request().postDataJSON();
      return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ trailer_override: false }) });
    });
    await select.selectOption("no");
    await expect.poll(() => body).toEqual({ trailer: false });
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    expect(res.violations.map((x) => x.id)).toEqual([]);
    await ctx.close();
  });
}

test("the efficiency view says how many drives it left out, and what they ran", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page, { scenario: "parked" });
  const b = (key: string, label: string, eff: number | null, drives: number) => ({ key, label, distance: drives * 18, energy_kwh: eff ? (drives * 18) / eff : 0, efficiency: eff, drives, not_enough_data: drives * 18 < 25 });
  await page.route("**/api/insights/efficiency*", (r) =>
    r.fulfill({
      json: {
        units: "imperial", distance_unit: "mi", efficiency_unit: "mi/kWh", min_bucket_distance: 25,
        total: b("total", "All drives", 2.31, 34),
        with_trailer: { key: "with_trailer", label: "With a trailer", distance: 120, energy_kwh: 85.7, efficiency: 1.4, drives: 2, not_enough_data: false },
        by_temperature: [b("lt32", "Below 32 °F", 1.62, 6), b("50_68", "50–68 °F", 2.28, 9)],
        by_speed: [b("city", "City (under 35 mph)", 2.6, 7), b("highway", "Highway (55–70 mph)", 2.15, 9)],
        by_drive_mode: [b("everyday", "All-Purpose", 2.38, 20), b("conserve", "Conserve", 2.62, 9)],
        excluded: { no_energy: 0, duplicates: 0, trailer: 2 },
      },
    }),
  );
  await page.addInitScript(() => localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 })));
  await page.goto("/activity?view=efficiency");
  await expect(page.locator("[data-eff-trailer]")).toContainText("Excludes 2 drives with a trailer, which ran 1.40 mi/kWh over 120 mi");
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(SHOTS, "trailer-efficiency-iphone.png"), fullPage: true });
  await ctx.close();
});
