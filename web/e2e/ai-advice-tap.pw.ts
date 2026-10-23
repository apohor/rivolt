import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// AI trip advice is sent only after an explicit tap that says what it sends. It used to fire on its own for trips
// over 200 miles or with a charging stop, which the privacy policy ("only if you turn it on") does not allow.

const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };

async function plan(page: Page, ai: boolean) {
  await stubApi(page);
  const advice: unknown[] = [];
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  await page.route("**/api/config", (r) =>
    r.fulfill(j({ features: { trip_planner_enabled: true, signup_enabled: true }, ai: { enabled: ai }, tiles: { url: "/api/maps/tiles/us.pmtiles", chargers_url: "/api/maps/tiles/chargers.pmtiles" }, valhalla: { path: "/api/maps/valhalla" }, impersonation: { disabled: true } })),
  );
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
  await page.route("**/api/trips/plan/advice", (r) => {
    advice.push(r.request().postDataJSON());
    return r.fulfill(j({ headline: "Two stops, one cheap", cost: [], efficiency: [], weather: [], vehicle: [], cost_estimate: { currency: "USD", dcfc_spend: 0, dcfc_spend_user_member: 0, dcfc_spend_all_member: 0 }, model: "test" }));
  });
  await page.goto("/trips/plan");
  // Load a trip only to fill From and To, then plan it fresh: a 270-mile route with a charging stop.
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  return advice;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`a long trip with a charging stop sends nothing until Get AI advice is tapped · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const advice = await plan(page, true);
    // Give anything automatic time to fire, then check none did.
    await page.waitForTimeout(1500);
    expect(advice).toHaveLength(0);
    const card = page.getByRole("heading", { name: "Trip analysis" }).locator("xpath=ancestor::*[self::section or self::div][1]");
    await expect(page.locator("[data-ai-disclosure]")).toContainText("Nothing is sent until you tap.");
    await expect(page.locator("[data-ai-disclosure]")).toContainText("charger names, your locations rounded to about 11 km, your tire pressures, your home electricity rate and the weather along the route");
    void card;
    await page.getByRole("button", { name: "Get AI advice" }).click();
    await expect.poll(() => advice.length).toBe(1);
    await expect(page.getByText("Analysis ready")).toBeVisible();
    await ctx.close();
  });
}

test("without an AI provider there is no advice card and nothing is sent", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const advice = await plan(page, false);
  await page.waitForTimeout(1200);
  await expect(page.getByRole("button", { name: "Get AI advice" })).toHaveCount(0);
  await expect(page.locator("[data-ai-disclosure]")).toHaveCount(0);
  expect(advice).toHaveLength(0);
  await ctx.close();
});
