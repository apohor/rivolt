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

// A multi-day plan stacks the map in the flow (the strip sits above it, not on it): the trip card stays at the ordinary bottom-left margin, not lifted.
test("desktop 1280: a multi-day plan's flow map keeps the trip card at the ordinary margin with radar on @webkit", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  const leg = (index: number, a: string, b: string) => ({
    index, plan: DEMO_PLAN, depart_at: `2027-01-2${index}T13:00:00Z`, arrive_at: `2027-01-2${index}T21:00:00Z`,
    start: { latitude: 41.88, longitude: -87.63, label: a }, end: { latitude: 41.59, longitude: -93.62, label: b },
    departure_soc: 90, arrival_soc: 24, destination_reached: true, soc_below_limit: false, costs: DEMO_PLAN.costs,
  });
  await page.route("**/api/config", (r) =>
    r.fulfill(j({
      features: { trip_planner_enabled: true, landing_enabled: false, demo_enabled: false, signup_enabled: true },
      tiles: { url: "/api/maps/tiles/us.pmtiles", chargers_url: "/api/maps/tiles/chargers.pmtiles", glyphs_url: "/api/maps/glyphs/{fontstack}/{range}.pbf", dem_url: "/api/maps/dem/{z}/{x}/{y}.png", dem_minzoom: 3, dem_maxzoom: 10, radar_url: "/api/maps/radar/{t}/{z}/{x}/{y}.png", radar_frame_url: "/api/maps/radar/frame", radar_minzoom: 3, radar_maxzoom: 9 },
      valhalla: { path: "/api/maps/valhalla" },
      impersonation: { disabled: true },
    })),
  );
  await page.route("**/api/maps/radar/frame", (r) => r.fulfill(j({ frame_unix: Math.floor(Date.now() / 1000) - 300 })));
  const md = { kind: "multiday", days: [leg(1, "Chicago", "Des Moines"), leg(2, "Des Moines", "Denver")], total: { distance_meters: 1_900_000, drive_duration_sec: 90_000, charging_duration_sec: 6_000 } };
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill(j([{ id: "st-md", name: "Two day", inputs: { ...INPUTS, extra_stops: [pt("Des Moines", 41.59, -93.62)], overnight_flags: [true] }, plan: md, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Two day/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await expect(page.locator("[data-day-card]").first()).toBeVisible();
  const strip = page.locator("[data-radar-label]").first();
  await expect(strip).toBeVisible();
  const lifted = await page.locator("[data-trip-map]").first().evaluate((el) => getComputedStyle(el.querySelector(".maplibregl-ctrl-bottom-left")!).bottom);
  await page.locator("[data-trip-map]").first().scrollIntoViewIfNeeded();
  await page.screenshot({ path: process.env.MD_SHOT || "/tmp/md.png" });
  expect(lifted).toBe("0px");
  await ctx.close();
});
