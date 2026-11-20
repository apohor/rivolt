import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Desktop: the radar legend sits at the very bottom of the map (owner, Nov 20), bottom left under the trip card, clear of the Chargers box, the card,
// the credits (bottom right), the zoom buttons and the Radar chip. The card is lifted above it.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Cleveland", 41.5, -81.7), destination: pt("Albany", 42.65, -73.75), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

for (const [w, h] of [[1280, 800], [1440, 900]]) {
test(`desktop ${w}: the radar legend sits at the bottom of the map, clear of the trip card and the credits @webkit`, async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: "dark" });
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
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  const strip = page.locator("[data-radar-label]");
  await expect(strip).toBeVisible();
  const box = async (l: import("@playwright/test").Locator) => {
    const b = await l.boundingBox();
    if (!b) throw new Error("no box");
    return b;
  };
  const s = await box(strip);
  const map = await box(page.locator("[data-map-canvas]"));
  const chips = await box(page.locator("[data-map-chips]"));
  const chip = await box(page.locator("[data-radar-chip]"));
  // At the bottom edge, inside the map, with a small margin.
  expect(s.y + s.height).toBeLessThanOrEqual(map.y + map.height);
  expect(map.y + map.height - (s.y + s.height)).toBeLessThanOrEqual(16);
  expect(s.x).toBeGreaterThanOrEqual(map.x);
  expect(s.x + s.width).toBeLessThanOrEqual(map.x + map.width);
  // Clear of the Chargers box and the Radar chip above it (it no longer rides beside the chip).
  expect(s.y).toBeGreaterThanOrEqual(chips.y + chips.height + 8);
  expect(s.y).toBeGreaterThanOrEqual(chip.y + chip.height + 8);
  const clear = (a: { x: number; y: number; width: number; height: number }, b: { x: number; y: number; width: number; height: number }) =>
    a.x + a.width <= b.x || b.x + b.width <= a.x || a.y + a.height <= b.y || b.y + b.height <= a.y;
  // Clear of the trip card (lifted above it), the credits and the zoom buttons.
  const card = page.locator(".rivolt-trip-card");
  await expect(card).toBeVisible();
  const c = await box(card);
  expect(clear(s, c)).toBe(true);
  expect(c.y + c.height).toBeLessThanOrEqual(s.y);
  const credits = await box(page.locator(".maplibregl-ctrl-attrib").first());
  expect(clear(s, credits)).toBe(true);
  expect(clear(s, await box(page.locator(".maplibregl-ctrl-zoom-in")))).toBe(true);
  await ctx.close();
});
}
