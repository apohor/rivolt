import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// A charger-layer request that fails (not one a chip toggle cancelled) is said in the chip row, and the next toggle clears it.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

test("a failed layer request says so in the chip row; a good one clears it", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  let fail = false;
  await page.route("**/api/maps/corridor-layers", (r) =>
    fail ? r.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "boom" }) }) : r.fulfill(j({ pois: [], count: 0, total: 0, asof: "", route_km: 0 })),
  );
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  const chips = page.locator("[data-map-chips]").first();
  await expect(chips.getByRole("button", { name: "Fast", exact: true })).toBeVisible();
  await expect(chips.locator("[data-layer-error]")).toHaveCount(0);
  fail = true;
  await chips.getByRole("button", { name: "Medium", exact: true }).click();
  await expect(chips.locator("[data-layer-error]")).toContainText("Couldn't load stations");
  fail = false;
  await chips.getByRole("button", { name: "Hotel", exact: true }).click();
  await expect(chips.locator("[data-layer-error]")).toHaveCount(0);
  await ctx.close();
});
