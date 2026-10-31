import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// The drive map as designed (Maps 1d): one smooth gradient scaled to the drive, a Speed / Efficiency / Elevation switch when the drive has the
// values, Start / End pills, a legend card, and a readout beside the scrub dot. One fixed, made-up drive near (10, 10) so shots compare across
// themes and widths; each shot waits for the route to be drawn (data-map-drawn above zero).
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const MIN = 60_000;
const START = e2eNow() - 3 * 3_600_000;

async function setup(page: Page, withExtras = true) {
  await stubApi(page);
  const drive = {
    ID: "drive-1", VehicleID: VEHICLE_ID,
    StartedAt: new Date(START).toISOString(), EndedAt: new Date(START + 30 * MIN).toISOString(),
    StartSoCPct: 80, EndSoCPct: 70, StartOdometerMi: 5000, EndOdometerMi: 5020, DistanceMi: 20,
    StartLat: 10, StartLon: 10, EndLat: 10.192, EndLon: 10.192,
    MaxSpeedMph: 78, AvgSpeedMph: 40, EnergyUsedKWh: 8, Source: "live",
  };
  // The battery falls faster on the climbs (a wave over the drive), so efficiency varies along the line: 80% to 70% overall.
  let soc = 80;
  const socAt = Array.from({ length: 121 }, (_, i) => {
    if (i > 0) soc -= 0.083 + 0.06 * Math.sin(i / 9);
    return soc;
  });
  const samples = Array.from({ length: 121 }, (_, i) => ({
    VehicleID: VEHICLE_ID, At: new Date(START + i * 15_000).toISOString(),
    BatteryLevelPct: socAt[i], RangeMi: 250, OdometerMi: 5000 + i / 6,
    Lat: 10 + i * 0.0016, Lon: 10 + i * 0.0016 + 0.01 * Math.sin(i / 14), SpeedMph: 30 + 25 * Math.abs(Math.sin(i / 17)),
    ShiftState: "D", ChargingState: "", ChargerPowerKW: 0, ChargeLimitPct: 80,
    InsideTempC: 21, OutsideTempC: 11, DriveNumber: 1, ChargeNumber: 0, Source: "live",
    ...(withExtras ? { altitude_m: 200 + 120 * Math.sin(i / 25) + i } : {}),
  }));
  await page.route("**/api/drives?**", (r) => r.fulfill({ json: [drive] }));
  await page.route("**/api/samples**", (r) => r.fulfill({ json: samples }));
}

const drawn = (page: Page) => page.locator("[data-map-drawn]:not([data-map-drawn='0'])").first();

for (const [vpName, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`drive map polish · ${vpName} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await setup(page);
      await page.goto("/drives/drive-1");
      await expect(drawn(page)).toBeAttached();
      const legend = page.locator(".rivolt-legend");
      await expect(legend).toBeVisible();
      // Speed, Efficiency and Elevation all qualify for this drive.
      await expect(legend.getByRole("group", { name: "Route colour" }).getByRole("button")).toHaveText(["Speed", "Efficiency", "Elevation"]);
      await legend.scrollIntoViewIfNeeded();
      await page.locator("[data-map-drawn]").first().screenshot({ path: path.join(SHOTS, `drive-map-polish-${vpName}-${theme}.png`) });
      await ctx.close();
    });
  }
}

test("iphone: the colour modes switch, repaint and label their legend", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await setup(page);
  await page.goto("/drives/drive-1");
  await expect(drawn(page)).toBeAttached();
  const legend = page.locator(".rivolt-legend");
  await legend.getByRole("button", { name: "Efficiency" }).click();
  await expect(legend).toHaveAttribute("data-mode", "eff");
  await expect(legend).toContainText("Efficiency (from battery change)");
  await expect(legend).toContainText("mi/kWh");
  await legend.getByRole("button", { name: "Elevation" }).click();
  await expect(legend).toHaveAttribute("data-mode", "elev");
  await expect(legend).toContainText(" ft");
  await legend.getByRole("button", { name: "Speed" }).click();
  await expect(legend).toContainText("mph");
  await ctx.close();
});

for (const mode of ["Speed", "Efficiency", "Elevation"] as const) {
  test(`drive map mode ${mode} · desktop · dark`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await page.addInitScript(() => localStorage.setItem("rivolt.theme", "dark"));
    await setup(page);
    await page.goto("/drives/drive-1");
    await expect(drawn(page)).toBeAttached();
    await page.locator(".rivolt-legend").getByRole("button", { name: mode }).click();
    await page.locator(".rivolt-legend").scrollIntoViewIfNeeded();
    await page.locator("[data-map-drawn]").first().screenshot({ path: path.join(SHOTS, `drive-map-mode-${mode.toLowerCase()}-desktop-dark.png`) });
    await ctx.close();
  });
}

test("iphone: a drive without altitude or battery values shows no switch, only Speed", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  await setup(page, false);
  // No battery values either: the samples carry a flat battery level, so Efficiency does not qualify.
  await page.route("**/api/samples**", (r) =>
    r.fulfill({
      json: Array.from({ length: 121 }, (_, i) => ({
        VehicleID: VEHICLE_ID, At: new Date(START + i * 15_000).toISOString(), BatteryLevelPct: 70, RangeMi: 250, OdometerMi: 5000 + i / 6,
        Lat: 10 + i * 0.0016, Lon: 10 + i * 0.0016, SpeedMph: 40 + (i % 9), ShiftState: "D", ChargingState: "", ChargerPowerKW: 0, ChargeLimitPct: 80,
        InsideTempC: 21, OutsideTempC: 11, DriveNumber: 1, ChargeNumber: 0, Source: "live",
      })),
    }),
  );
  await page.goto("/drives/drive-1");
  await expect(drawn(page)).toBeAttached();
  await expect(page.locator(".rivolt-legend")).toHaveAttribute("data-mode", "speed");
  await expect(page.locator(".rivolt-legend").getByRole("group", { name: "Route colour" })).toHaveCount(0);
  await ctx.close();
});

// The hover readout: a dot on the route and a pill beside it (time, the active mode's value with units), from hovering the chart at a fixed
// fraction. The viewport is tall enough to hold the map and the chart, so the shot is taken without scrolling (which would end the hover).
for (const [name, vp, theme] of [
  ["desktop", { width: 1280, height: 1500 }, "dark"],
  ["desktop", { width: 1280, height: 1500 }, "light"],
  ["iphone", { width: 390, height: 1700 }, "dark"],
] as const) {
  test(`${name === "iphone" ? "iphone: " : ""}drive map hover readout · ${name} · ${theme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
    await setup(page);
    await page.goto("/drives/drive-1");
    await expect(drawn(page)).toBeAttached();
    // The timeline charts are SVG with a transparent pointer overlay (crosshair cursor); hovering it moves the map's scrub dot.
    const over = page.locator("rect[style*='crosshair']").first();
    await expect(over).toBeVisible();
    const box = (await over.boundingBox())!;
    await page.mouse.move(box.x + box.width * 0.3, box.y + box.height / 2);
    await page.mouse.move(box.x + box.width * 0.4, box.y + box.height / 2);
    const readout = page.locator(".rivolt-readout");
    await expect(readout).toBeVisible();
    await expect(readout).toContainText(/\d{1,2}:\d{2}/);
    await expect(readout).toContainText("mph");
    const map = (await page.locator("[data-map-drawn]").first().boundingBox())!;
    await page.screenshot({ path: path.join(SHOTS, `drive-map-hover-${name}-${theme}.png`), clip: map });
    await ctx.close();
  });
}
