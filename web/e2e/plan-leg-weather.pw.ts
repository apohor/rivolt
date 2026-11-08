import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// The weather on a planned route: a badge on each leg (temperature, wind along the way you drive, "snow expected"), a "Weather" chip that
// turns them off, a tap that opens the leg's details, and the same facts as tags on the stop rows. Units follow the account (°F and mph here).
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
      { multiplier: 1.14, temp_c: 5, headwind_kph: 40, precip_mm: 2 },
      { multiplier: 1.2, temp_c: -6, headwind_kph: -20, precip_mm: 3, snow: true },
    ],
  },
};

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`@webkit leg weather badges, chip and stop tags · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
    await page.route("**/api/trips/plan", (r) => r.fulfill(j(PLAN)));
    await page.goto("/trips/plan");
    await page.getByRole("tab", { name: "Saved" }).click();
    await page.getByRole("button", { name: /Old trip/ }).click();
    await page.getByRole("tab", { name: "Planner" }).click();
    await page.getByRole("button", { name: "Plan trip" }).click();
    await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();

    // The stop rows carry the tags; the estimate line is not on the row.
    const tags = page.locator("[data-leg-weather]");
    await expect(tags.nth(0)).toHaveText("41 °F · 25 mph headwind · rain");
    await expect(tags.nth(1)).toHaveText("21 °F · 12 mph tailwind · snow expected");
    await expect(page.locator("[data-plan-timeline]")).not.toContainText("(estimate)");

    // The badges: facts only, no energy figure.
    const badges = page.locator("[data-leg-badge]");
    await expect(badges).toHaveCount(2);
    await expect(badges.nth(0)).toContainText("41°");
    await expect(badges.nth(0)).toContainText("25 mph");
    await expect(badges.nth(1)).toContainText("snow expected");
    await expect(page.locator("[data-leg-badge]").first()).not.toContainText("%");

    // A tap on a badge opens that leg's details, with the one estimate line.
    await badges.nth(0).click();
    const pop = page.locator("[data-leg-popup]");
    await expect(pop).toContainText("Los Angeles to Sample Fast Charge");
    await expect(pop).toContainText("Weather and your setup: +14% (estimate)");

    // The chip turns them off and on.
    const chip = page.getByRole("button", { name: "Weather", exact: true });
    await expect(chip).toHaveAttribute("aria-pressed", "true");
    await chip.click();
    await expect(badges).toHaveCount(0);
    await chip.click();
    await expect(badges).toHaveCount(2);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await ctx.close();
  });
}
