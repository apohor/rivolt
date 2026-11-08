import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Two pins the person dropped are both labelled "A place you chose": the Via rows' buttons still tell them apart by position (#1655), and a stop of the
// person's own is headed "Your stop" on the map, not "Charging stop" (#1656).
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

test("Via rows: two dropped pins have different accessible button names", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  await page.waitForTimeout(1500);
  const map = page.locator("[data-plan-area='map'] .maplibregl-map").first();
  const mb = (await map.boundingBox())!;
  for (const [fx, fy] of [[0.3, 0.6], [0.7, 0.5]]) {
    await expect(async () => {
      await page.mouse.click(mb.x + mb.width * fx, mb.y + mb.height * fy, { button: "right" });
      await expect(page.getByRole("button", { name: "Add as a stop" })).toBeVisible({ timeout: 1500 });
    }).toPass({ timeout: 10_000 });
    await page.getByRole("button", { name: "Add as a stop" }).click();
    await page.waitForTimeout(800);
  }
  await page.getByRole("button", { name: /More options/ }).first().click();
  const moves = page.getByRole("button", { name: /^Move stop \d of 2, A place you chose, (earlier|later)$/ });
  await expect(moves).toHaveCount(4);
  const names = await moves.evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")));
  expect(new Set(names).size, `names: ${names.join(" | ")}`).toBe(4);
  await expect(page.getByRole("button", { name: "remove stop 1: A place you chose" })).toBeVisible();
  await expect(page.getByRole("button", { name: "remove stop 2: A place you chose" })).toBeVisible();
  await ctx.close();
});
