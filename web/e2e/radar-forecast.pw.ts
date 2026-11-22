import fs from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// The forecast radar along the route (design key radar_forecast). Pro: "Rain likely at arrival" / "Snow likely at arrival" on the stop row, in
// place of the plain rain tag, with the NOAA RRFS credit. Free: one quiet teaser line at the plan summary, only when a leg is wet. "dry" and an
// absent field show nothing. Key off: the plan is as before. RADAR_SHOTS=<dir> also writes the owner's screenshots.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Las Vegas", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
const ME = { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, billing_enabled: true, history_days: null, plan_trial_ends: null, plan_cancels_at: null, plan_grace_ends: null, plan_ended_at: null, plan_ended_source: null };
const PRO = { ...ME, plan: "pro", plan_source: "stripe", plan_period_end: "2026-12-01T00:00:00Z" };
const FREE = { ...ME, plan: "free", plan_source: null, plan_period_end: null, history_days: 90 };
const adj = (extra: Record<string, unknown>, legs: Record<string, unknown>[]) => ({
  ...DEMO_PLAN,
  weather_adjustment: { adjusted_arrival_soc: [34, 18], final_arrival_soc: 18, below_target: false, target_arrival_soc: 10, legs, ...extra },
});
const LEG1 = { multiplier: 1.05, temp_c: 5, air_temp_c: 5, precip_mm: 2 };
const LEG2 = { multiplier: 1.02, temp_c: 8, air_temp_c: 8 };
const RAIN = adj({}, [{ ...LEG1, radar: { level: "rain" } }, { ...LEG2, radar: { level: "snow" } }]);
const DRY = adj({}, [{ ...LEG1, radar: { level: "dry" } }, { ...LEG2, radar: { level: "dry" } }]);
const ABSENT = adj({}, [LEG1, LEG2]);
const TEASE = adj({ radar_teaser: { wet_legs: 2, legs: 2 } }, [LEG1, LEG2]);
const TEASE0 = adj({ radar_teaser: { wet_legs: 0, legs: 2 } }, [LEG1, LEG2]);

async function open(page: Page, me: unknown, plan: unknown, keys: string[] = ["radar_forecast"]) {
  await stubApi(page, { designPreview: keys });
  await page.route("**/api/auth/me", (r) => r.fulfill(j(me)));
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(plan)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
}

const tags = (page: Page) => page.locator("[data-leg-weather]");

test("Pro with rain and snow: the forecast tag replaces the plain rain tag, with the credit", async ({ page }) => {
  await open(page, PRO, RAIN);
  await expect(tags(page).nth(0)).toHaveText("41°F · Rain likely at arrival");
  await expect(tags(page).nth(1)).toHaveText("46°F · Snow likely at arrival");
  await expect(page.locator("[data-plan-timeline]")).not.toContainText(/\brain\b(?! likely)/);
  await expect(page.getByTestId("radar-credit")).toHaveText("Rain and snow likely at arrival: NOAA RRFS");
  await expect(page.locator("[data-radar-teaser]")).toHaveCount(0);
});

test("Pro, dry: nothing from the forecast, the plan is as before", async ({ page }) => {
  await open(page, PRO, DRY);
  await expect(tags(page).nth(0)).toHaveText("41°F · rain");
  await expect(page.locator("[data-plan-timeline]")).not.toContainText(/dry|likely at arrival/i);
  await expect(page.getByTestId("radar-credit")).toHaveCount(0);
});

test("Pro, forecast field absent (service down or stale): nothing, never dry", async ({ page }) => {
  await open(page, PRO, ABSENT);
  await expect(tags(page).nth(0)).toHaveText("41°F · rain");
  await expect(page.locator("[data-plan-timeline]")).not.toContainText(/dry|likely at arrival/i);
  await expect(page.getByTestId("radar-credit")).toHaveCount(0);
  await expect(page.locator("[data-radar-teaser]")).toHaveCount(0);
});

test("key off: a Pro plan with forecast tags looks as it did", async ({ page }) => {
  await open(page, PRO, RAIN, []);
  await expect(tags(page).nth(0)).toHaveText("41°F · rain");
  await expect(page.getByTestId("radar-credit")).toHaveCount(0);
});

test("free with wet legs: one teaser line, no stop tags, the Pro sheet, dismissed for the plan", async ({ page }) => {
  await open(page, FREE, TEASE);
  const t = page.locator("[data-radar-teaser]");
  await expect(t).toHaveCount(1);
  await expect(t).toContainText("Rain or snow is likely at 2 stops on this route. The Pro tier shows which.");
  await expect(page.locator("[data-plan-timeline]")).not.toContainText("likely at arrival");
  await expect(page.getByTestId("radar-credit")).toHaveCount(0);
  await t.getByRole("button", { name: "See Pro" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.keyboard.press("Escape");
  await t.getByRole("button", { name: "Dismiss" }).click();
  await expect(t).toHaveCount(0);
  // The same plan again: still dismissed.
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  await expect(page.locator("[data-radar-teaser]")).toHaveCount(0);
});

test("free with no wet legs: no teaser", async ({ page }) => {
  await open(page, FREE, TEASE0);
  await expect(page.locator("[data-radar-teaser]")).toHaveCount(0);
});

const SHOTS = process.env.RADAR_SHOTS;
for (const w of [393, 1440]) for (const scheme of ["light", "dark"] as const) {
  test(`screenshots ${w} ${scheme}`, async ({ browser }) => {
    test.skip(!SHOTS, "RADAR_SHOTS not set");
    fs.mkdirSync(SHOTS!, { recursive: true });
    for (const [name, me, plan] of [["pro-rain-snow", PRO, RAIN], ["free-teaser", FREE, TEASE]] as const) {
      const ctx = await browser.newContext({ viewport: { width: w, height: w < 500 ? 852 : 900 }, colorScheme: scheme });
      const page = await ctx.newPage();
      await open(page, me, plan);
      if (w < 500) {
        const handle = page.locator("[data-plan-sheet-handle]");
        await handle.click();
        await handle.click();
        await expect(page.locator("[data-plan-sheet]")).toHaveAttribute("data-snap", "full");
      }
      const area = page.locator(name === "free-teaser" ? '[data-plan-area="summary"]' : '[data-plan-area="itinerary"]');
      await area.scrollIntoViewIfNeeded();
      await area.screenshot({ path: `${SHOTS}/${name}-${w}-${scheme}.png` });
      await ctx.close();
    }
  });
}

// plan_v2 takes the weather chips off the map: the rain or snow line then lives only in the itinerary rows, and it must stay there for Pro, and the
// free teaser must stay at the plan summary. At phone and desktop widths.
for (const w of [393, 1440]) {
  test(`plan_v2 on, Pro, ${w}px: no weather chip on the map, the itinerary rows still say rain or snow likely, with the credit`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: w === 393 ? 852 : 900 });
    await open(page, PRO, RAIN, ["radar_forecast", "plan_v2"]);
    await expect(page.locator("[data-weather-chip], [data-leg-badge]")).toHaveCount(0);
    await expect(tags(page).nth(0)).toHaveText("41°F · Rain likely at arrival");
    await expect(tags(page).nth(1)).toHaveText("46°F · Snow likely at arrival");
    await expect(page.getByTestId("radar-credit")).toHaveText("Rain and snow likely at arrival: NOAA RRFS");
  });

  test(`plan_v2 on, free with wet legs, ${w}px: the teaser line is still there and no stop shows a forecast tag`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: w === 393 ? 852 : 900 });
    await open(page, FREE, TEASE, ["radar_forecast", "plan_v2"]);
    await expect(page.locator("[data-radar-teaser]")).toContainText("Rain or snow is likely at 2 stops");
    await expect(page.locator("[data-plan-timeline]")).not.toContainText(/likely at arrival/);
  });
}
