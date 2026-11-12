import { expect, test } from "@playwright/test";
import { routeInStrip } from "./routeStrip";
import { DEMO_PLAN, stubApi } from "./stub";

// #1980: at Safari's largest text (aA 300 %: 131 x 232 CSS px) the strip of map above the sheet is too short for its controls (tight). The leg weather
// badges covered the route there and could not be turned off, the radar legend and the layer toggles were out of reach (#1975, #1976), and Map was
// not offered at the peek. Now the badges are not drawn there (each stop's card has the weather), one Layers button opens the toggles and the radar
// legend in a panel, and Map at the peek lowers the sheet to its handle.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Las Vegas", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
const PLAN = {
  ...DEMO_PLAN,
  weather_adjustment: {
    adjusted_arrival_soc: [34, 18],
    final_arrival_soc: 18,
    below_target: false,
    target_arrival_soc: 10,
    profile_multiplier: 1.03,
    legs: [
      { multiplier: 1.14, temp_c: 3, air_temp_c: 5, headwind_kph: 40, precip_mm: 2 },
      { multiplier: 1.2, temp_c: -6, headwind_kph: -20, precip_mm: 3, snow: true },
    ],
  },
};

test("@webkit phone at aA 300%: tight map hides the badges, its layers open in a panel, Map lowers the sheet from the peek", async ({ browser }, info) => {
  const ctx = await browser.newContext({ viewport: { width: 131, height: 232 }, colorScheme: "dark" });
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
  const sheet = page.locator("[data-plan-sheet]");
  await expect(sheet).toHaveAttribute("data-snap", "peek");
  await expect(page.locator("[data-trip-map][data-map-tight]")).toBeAttached();
  // Drawn once the map is up: the weather chip is on (the trip planner settings' default) and the plan has leg weather.
  await expect(page.locator("[data-map-drawn]:not([data-map-drawn='0'])")).toBeAttached();
  await page.waitForTimeout(1500);
  // The page brought the map up just under the top bar, not under it with the Layers button hidden there (#1984).
  const gap = () => page.evaluate(() => Math.round(document.querySelector("[data-plan-area='map']")!.getBoundingClientRect().top - document.querySelector("header.sticky")!.getBoundingClientRect().bottom));
  await expect.poll(gap).toBeGreaterThanOrEqual(6);
  const shot = async (name: string) => {
    const p = info.outputPath(`${name}.png`);
    await page.screenshot({ path: p });
    await info.attach(name, { path: p, contentType: "image/png" });
  };
  await shot("tight-300-peek");

  // The badges are not drawn over the strip, and neither is the radar legend (on by default here).
  await expect(page.locator("[data-leg-badge]")).toHaveCount(0);
  await expect(page.locator("[data-radar-label]")).toBeHidden();

  // Layers opens the charger toggles and the radar, with its legend, in a panel inside the screen; Done closes it and gives the focus back.
  const layers = page.getByRole("button", { name: "Map layers" });
  await expect(layers).toBeVisible();
  await layers.click();
  const panel = page.getByRole("dialog", { name: "Map layers" });
  await expect(panel).toBeVisible();
  const pb = (await panel.boundingBox())!;
  expect(pb.x).toBeGreaterThanOrEqual(0);
  expect(pb.x + pb.width).toBeLessThanOrEqual(131.5);
  const radar = panel.getByRole("button", { name: "Radar", exact: true });
  await expect(radar).toHaveAttribute("aria-pressed", "true");
  await expect(panel.locator("[data-radar-label]")).toBeVisible();
  const fast = panel.getByRole("button", { name: "Fast", exact: true });
  await expect(fast).toHaveAttribute("aria-pressed", "true");
  await fast.click();
  await expect(fast).toHaveAttribute("aria-pressed", "false");
  await radar.click();
  await expect(radar).toHaveAttribute("aria-pressed", "false");
  await shot("tight-300-layers");
  await panel.getByRole("button", { name: "Done" }).click();
  await expect(panel).toBeHidden();
  await expect(layers).toBeFocused();

  // Map shows at the peek and lowers the sheet to its handle; the route is framed in the taller strip, and the handle raises the sheet again.
  const map = page.locator("[data-plan-sheet-map]");
  await expect(map).toBeVisible();
  await map.click();
  await expect(sheet).toHaveAttribute("data-snap", "min");
  await page.waitForTimeout(1500);
  await shot("tight-300-min");
  expect((await sheet.boundingBox())!.height).toBeLessThanOrEqual(40);
  await expect(map).toHaveCount(0);
  const got = await routeInStrip(page);
  expect(got.n, `route pixels in the ${got.w}x${got.h} strip`).toBeGreaterThan(30);
  expect(got.span).toBeGreaterThanOrEqual(Math.min(got.w, got.h) * 0.5);
  await page.locator("[data-plan-sheet-handle]").click();
  await expect(sheet).toHaveAttribute("data-snap", "peek");
  await ctx.close();
});
