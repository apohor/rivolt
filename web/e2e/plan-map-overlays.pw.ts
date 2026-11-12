import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// What lies over the planner's map on a phone, with the weather and the radar on: the radar strip rides the sheet's top edge instead of floating over the
// route, the leg badges stay inside the map and off each other, and the weather credit sits with the itinerary in the sheet, not under it.
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
const box = async (loc: import("@playwright/test").Locator) => {
  const b = await loc.boundingBox();
  if (!b) throw new Error("no box");
  return { x0: b.x, y0: b.y, x1: b.x + b.width, y1: b.y + b.height };
};
const overlap = (a: { x0: number; y0: number; x1: number; y1: number }, b: { x0: number; y0: number; x1: number; y1: number }) => a.x0 < b.x1 && a.x1 > b.x0 && a.y0 < b.y1 && a.y1 > b.y0;

test("@webkit phone: radar strip, leg badges and the weather credit stay clear of each other", async ({ browser }, info) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
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
  await expect(page.locator("[data-leg-badge]").first()).toBeVisible();
  await expect(page.getByRole("button", { name: "Radar", exact: true })).toHaveAttribute("aria-pressed", "true"); // on by default
  const strip = page.locator("[data-radar-label]");
  await expect(strip).toBeVisible();
  await page.screenshot({ path: info.outputPath("plan-map-overlays.png") });
  await info.attach("plan-map-overlays", { path: info.outputPath("plan-map-overlays.png"), contentType: "image/png" });

  const map = await box(page.locator("[data-plan-area='map']"));
  const s = await box(strip);
  // One line, riding just above the sheet's top edge at the map's left, inside the screen, and over no pin or badge (every marker on the map).
  expect(s.y1 - s.y0).toBeLessThan(26);
  const sheet = await box(page.locator("[data-plan-sheet]"));
  expect(s.y1).toBeLessThanOrEqual(sheet.y0);
  expect(sheet.y0 - s.y1).toBeLessThan(10);
  expect(s.x0).toBeGreaterThanOrEqual(map.x0);
  expect(s.x1).toBeLessThanOrEqual(390 - 8);
  await expect(strip).toHaveAccessibleName(/^light, moderate, heavy, NOAA\/NWS radar, \d/);
  const markers = page.locator("[data-trip-map] .maplibregl-marker");
  for (let i = 0; i < (await markers.count()); i++) {
    const m = await box(markers.nth(i));
    expect(overlap(s, m), `marker ${i} at ${JSON.stringify(m)} under the strip ${JSON.stringify(s)}`).toBe(false);
  }
  const badges = page.locator("[data-leg-badge]");
  const n = await badges.count();
  const bs = [];
  for (let i = 0; i < n; i++) {
    const b = await box(badges.nth(i));
    expect(b.x0).toBeGreaterThanOrEqual(map.x0);
    expect(b.x1).toBeLessThanOrEqual(map.x1);
    bs.push(b);
  }
  for (let i = 0; i < bs.length; i++) for (let k = i + 1; k < bs.length; k++) expect(overlap(bs[i], bs[k])).toBe(false);
  // The start pill (a map symbol) cannot be measured; the summary card's room is reserved in the fit, and the card is not under the strip or a badge.
  const card = page.locator(".rivolt-trip-card");
  if (await card.count()) {
    const c = await box(card);
    expect(overlap(s, c)).toBe(false);
    for (const b of bs) expect(overlap(b, c)).toBe(false);
  }
  // The weather credit is with the itinerary.
  await expect(page.locator("[data-plan-area='itinerary'] [data-weather-credit]")).toHaveCount(1);
});
