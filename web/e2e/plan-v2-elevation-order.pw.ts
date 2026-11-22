import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// plan_v2: Save trip sits directly under the itinerary, then the elevation profile (key off: Save trip leads, the profile sits between the summary and the itinerary, as before). The profile's link to the map stays.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
const TOTAL = 434_500;
const PROFILE = {
  points: Array.from({ length: 41 }, (_, i) => [Math.round((TOTAL * i) / 40), Math.round(300 + 600 * Math.sin((i / 40) * Math.PI * 2) ** 2)]),
  waypoint_at_m: [0, 215_000, TOTAL],
};
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

async function openSaved(page: Page, key: boolean, plan: unknown) {
  await stubApi(page, key ? { designPreview: ["plan_v2"] } : {});
  const t = DEMO_PRESETS[0];
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-v2", name: t.name, inputs: t.inputs, plan, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /LA to Las Vegas/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await expect(page.locator("[data-plan-area='map'] .maplibregl-marker").first()).toBeVisible();
}

// "after" is true when `b` follows `a` in the page.
const follows = (page: Page, a: string, b: string) =>
  page.evaluate(([x, y]) => !!(document.querySelector(x)!.compareDocumentPosition(document.querySelector(y)!) & Node.DOCUMENT_POSITION_FOLLOWING), [a, b]);

for (const [w, h] of [[393, 852], [1440, 900]]) {
  for (const key of [true, false]) {
    test(`${w}px · key ${key ? "on" : "off"}: the profile is ${key ? "after" : "before"} the itinerary`, async ({ page }) => {
      await page.setViewportSize({ width: w, height: h });
      await openSaved(page, key, { ...DEMO_PLAN, elevation_profile: PROFILE });
      if (w < 500) await page.locator("[data-plan-sheet-handle]").click();
      await expect(page.locator("[data-elevation-chart]")).toHaveCount(1);
      const sel = "[data-plan-area=itinerary]";
      expect(await follows(page, "[data-plan-area=summary]", "[data-elevation-chart]")).toBe(true);
      expect(await follows(page, sel, "[data-elevation-chart]")).toBe(key);
      // Save trip: key on, right after the itinerary and before the profile; key off, ahead of everything.
      await expect(page.locator("[data-save-trip]")).toHaveCount(1);
      expect(await follows(page, sel, "[data-save-trip]")).toBe(key);
      expect(await follows(page, "[data-save-trip]", "[data-elevation-chart]")).toBe(true);
      if (key) {
        // Nothing but the Save trip bar sits between the itinerary and the profile.
        expect(await page.evaluate(() => document.querySelector("[data-plan-area=itinerary]")!.nextElementSibling === document.querySelector("[data-save-trip]"))).toBe(true);
        expect(await page.evaluate(() => document.querySelector("[data-save-trip]")!.nextElementSibling?.contains(document.querySelector("[data-elevation-chart]")) ?? false)).toBe(true);
      }
      if (key) {
        // The link to the map stays: pointing at the chart puts the dot on the route.
        const chart = page.locator("[data-elevation-chart]");
        await chart.scrollIntoViewIfNeeded();
        const box = (await chart.boundingBox())!;
        await page.mouse.move(box.x + box.width * 0.5, box.y + box.height * 0.5);
        await expect(page.locator("[data-elevation-dot]")).toBeVisible();
        await expect(page.locator("[data-elevation-stop]")).toHaveCount(1);
        await page.screenshot({ path: path.join(SHOTS, `elevation-below-${w}.png`) });
      }
    });
  }
}

test("key on, no elevation data: no chart and the itinerary is intact", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, true, DEMO_PLAN);
  await expect(page.locator("[data-plan-area=itinerary]")).toBeVisible();
  await expect(page.locator("[data-elevation-chart]")).toHaveCount(0);
  // The day cards carry the charge ring too, on the rail and on each day's map.
  await expect(page.locator("[data-plan-timeline] [data-charge-ring-rail]").first()).toBeVisible();
  await expect(page.locator(".maplibregl-marker [data-charge-ring]").first()).toBeVisible();
});

test("key on, multi-day: the day cards carry no profile, as before", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await stubApi(page, { designPreview: ["plan_v2"] });
  const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
  const withElev = { ...DEMO_PLAN, elevation_profile: PROFILE };
  const day = (index: number, from: string, to: string) => ({
    index, plan: withElev, depart_at: `2026-10-2${index}T13:00:00Z`, arrive_at: `2026-10-2${index}T21:00:00Z`,
    start: { latitude: 41.88, longitude: -87.63, label: from }, end: { latitude: 41.59, longitude: -93.62, label: to },
    departure_soc: 80, arrival_soc: 24, destination_reached: true, soc_below_limit: false, costs: DEMO_PLAN.costs,
  });
  const body = { days: [day(1, "Chicago", "Des Moines"), day(2, "Des Moines", "Denver")], total: { distance_meters: 1_900_000, drive_duration_sec: 26_400, charging_duration_sec: 3_000 }, tz: "UTC" };
  await page.route("**/api/trips/plan-multiday", (r) => r.fulfill(j(body)));
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill(j([{ id: "st-days", name: "Chicago to Denver", created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z",
      inputs: { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [pt("Peoria", 40.69, -89.59), pt("Des Moines", 41.59, -93.62)], overnight_flags: [false, true], max_overnight_soc_pct: 80, overnight_departure_local: "07:30", overnight_l2_kw: 7 } }])),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Chicago to Denver/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: /Plan 2-day trip/ }).click();
  await expect(page.getByText(/Day 2/).first()).toBeVisible();
  // The day cards have no profile today (only a single-day plan does), so there is nothing to move and nothing to break.
  await expect(page.locator("[data-elevation-chart]")).toHaveCount(0);
});
