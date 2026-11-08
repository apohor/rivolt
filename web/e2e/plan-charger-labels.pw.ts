import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// One set of charger-type words (Fast, Medium, Hotel, Camping) on the planner's chips and stops, and an (i) that explains them on a tap, not a hover.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`@webkit iphone: the layer chips use the charger words and the (i) explains them · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
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
    const chips = page.locator("[data-map-chips]").first();
    for (const name of ["Fast", "Medium", "Hotel", "All"]) await expect(chips.getByRole("button", { name, exact: true })).toBeVisible();
    await expect(chips).not.toContainText(/Level 2|Hotels with|Fast charging/);
    // The (i): a tap opens one sentence per type, Escape closes it and gives focus back.
    const info = chips.getByRole("button", { name: "What the charger types mean" });
    await info.click();
    const group = page.getByRole("group", { name: "What the charger types mean" });
    await expect(group).toBeVisible();
    await expect(group).toContainText("DC fast charging, usually 50 kW and up");
    await expect(group).toContainText("Level 2 (AC), 7 to 11 kWh per hour.");
    await expect(group).toContainText("A hotel with a listed Level 2 charger, which may be taken or out of date");
    await page.keyboard.press("Escape");
    await expect(group).toHaveCount(0);
    await expect(info).toBeFocused();
    // The charging stop in the itinerary says what kind it is.
    await expect(page.locator("[data-charger-type]").first()).toHaveText(/^(Fast|Medium) · \d+ kW$/i);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await ctx.close();
  });
}
