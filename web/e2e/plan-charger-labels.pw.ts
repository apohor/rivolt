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

// The (i) popover on a phone: it is drawn in the page, inside the window, never cut off by the map it opened over (the Hotel sentence was clipped at the
// map's bottom edge), and a tap anywhere outside closes it, including a tap on the map itself.
async function planned(browser: import("@playwright/test").Browser, vp: { width: number; height: number }, touch = false) {
  // Reduced motion: the planner's reveal scroll (TripPlanPage) is then instant, not an animation a click can interrupt.
  const ctx = await browser.newContext({ viewport: vp, colorScheme: "dark", hasTouch: touch, reducedMotion: "reduce" });
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
  await expect(page.locator("[data-map-chips]").first()).toBeVisible();
  // The planner brings the new plan's map into view after two frames (TripPlanPage). A scroll the test makes itself (Playwright scrolling a button
  // into view before a click) must not race it, or the chips can be left under the header or the sheet: wait until the page has stopped scrolling.
  await page.waitForFunction(() => new Promise<boolean>((done) => {
    let last = -1, still = 0;
    const tick = () => {
      still = window.scrollY === last ? still + 1 : 0;
      last = window.scrollY;
      if (still >= 15) done(true);
      else requestAnimationFrame(tick);
    };
    tick();
  }));
  return { ctx, page };
}

for (const vp of [
  { width: 390, height: 844 },
  { width: 390, height: 560 },
  { width: 320, height: 640 },
]) {
  test(`@webkit iphone: the (i) popover stays inside the window, whole or scrollable · ${vp.width}x${vp.height}`, async ({ browser }) => {
    const { ctx, page } = await planned(browser, vp);
    await page.locator("[data-map-chips]").first().getByRole("button", { name: "What the charger types mean" }).click();
    const pop = page.locator("[data-charger-info-popover]");
    await expect(pop).toBeVisible();
    // The last sentence on the chips (Hotel) is reachable: all of it shows, or the popover scrolls.
    await expect(pop).toContainText("If it's free and working, a night adds 100 miles or more.");
    const b = (await pop.boundingBox())!;
    expect(b.x, "left edge").toBeGreaterThanOrEqual(0);
    expect(b.y, "top edge").toBeGreaterThanOrEqual(0);
    expect(b.x + b.width, "right edge").toBeLessThanOrEqual(vp.width);
    expect(b.y + b.height, "bottom edge").toBeLessThanOrEqual(vp.height);
    const { sh, ch } = await pop.evaluate((el) => ({ sh: el.scrollHeight, ch: el.clientHeight }));
    if (sh > ch) {
      await pop.evaluate((el) => el.scrollTo({ top: el.scrollHeight }));
      const last = (await pop.locator("p").last().boundingBox())!;
      expect(last.y + last.height, "the last sentence scrolls into view").toBeLessThanOrEqual(b.y + b.height + 1);
    }
    await ctx.close();
  });
}

test("@webkit iphone: a tap on the map closes the (i) popover, and so does a tap on the page", async ({ browser }) => {
  const { ctx, page } = await planned(browser, { width: 390, height: 844 }, true);
  const info = page.locator("[data-map-chips]").first().getByRole("button", { name: "What the charger types mean" });
  const pop = page.locator("[data-charger-info-popover]");
  for (const where of ["map", "page"]) {
    await info.tap();
    await expect(pop).toBeVisible();
    await expect(info).toHaveAttribute("aria-expanded", "true");
    if (where === "map") {
      const m = (await page.locator("[data-plan-area='map'] .maplibregl-map").first().boundingBox())!;
      await page.touchscreen.tap(m.x + m.width * 0.5, m.y + m.height * 0.7);
    } else {
      await page.touchscreen.tap(200, 8);
    }
    await expect(pop).toHaveCount(0);
    await expect(info).toHaveAttribute("aria-expanded", "false");
  }
  // A tap inside the popover leaves it open.
  await info.tap();
  await pop.locator("p").first().tap();
  await expect(pop).toBeVisible();
  await ctx.close();
});

