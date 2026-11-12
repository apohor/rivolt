import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Desktop: the radar legend rides next to the Radar chip, clear of the Chargers box above it (it used to sit right under that box and crowd it).
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Cleveland", 41.5, -81.7), destination: pt("Albany", 42.65, -73.75), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

test("desktop: the radar legend sits beside the Radar chip, clear of the Chargers box", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: "dark" });
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
  const chips = await box(page.locator("[data-map-chips]"));
  const chip = await box(page.locator("[data-radar-chip]"));
  expect(s.y).toBeGreaterThanOrEqual(chips.y + chips.height + 8); // clear of the Chargers box
  expect(s.x).toBeGreaterThanOrEqual(chip.x + chip.width); // beside the Radar chip, not under it
  expect(Math.abs(s.y + s.height / 2 - (chip.y + chip.height / 2))).toBeLessThan(4); // on its row
});
