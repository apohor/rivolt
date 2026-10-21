import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// Phone, single-day plan: the trip-summary sheet is fixed at the bottom, so the page needs room under its content or the
// last card (here "Where to sleep", with the reservation warning and the Recreation.gov credit) sits under the sheet.
const LONG = { ...DEMO_PLAN, Routes: [{ ...DEMO_PLAN.Routes[0], TotalDriveDurationSec: 40_000 }] };
const CANDIDATES = {
  candidates: [
    {
      kind: "campground", name: "Convict Lake Campground", lat: 37.59, lon: -118.86, l2_kw: 7.2, l2_kw_assumed: true, detour_m: 4000, along_m: 300000,
      connector: "nema_14_50", needs_adapter: false, electric_sites: 12, sites: 88, reservation_required: true, fee: "$30 per night",
      source: "Recreation.gov", source_url: "https://www.recreation.gov/camping/campgrounds/232447", asof: "2026-10-20",
    },
  ],
  target: { lat: 37.6, lon: -118.9, along_m: 302000, reaches_end: false },
  route_m: 500000,
  source: "listing",
};

for (const scheme of ["dark", "light"] as const) {
  test(`phone: the last card clears the sheet's peek · ${scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: scheme });
    const page = await ctx.newPage();
    await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
    await stubApi(page);
    await page.route("**/api/trips/overnight-candidates", (r) => r.fulfill({ json: CANDIDATES }));
    await page.route("**/api/geocode*", (r) => r.fulfill({ json: [{ name: new URL(r.request().url()).searchParams.get("q") ?? "", admin1: "US", country: "", latitude: 41.26, longitude: -95 }] }));
    await page.route("**/api/trips/plan", (r) => r.fulfill({ json: LONG }));
    await page.goto("/trips/plan");
    const bar = page.locator("[data-plan-bar]");
    await bar.locator("[data-plan-field][data-heading='From']").getByRole("button", { name: /Home/ }).click();
    await bar.locator("[data-plan-field][data-heading='To']").getByPlaceholder(/To/).fill("Omaha");
    await page.getByRole("button", { name: /Omaha/ }).first().click();
    await page.getByRole("button", { name: "Plan trip" }).click();
    const card = page.locator("[data-overnight-suggestions]");
    await expect(card).toBeVisible();
    await expect(card.locator("[data-stay-warning]")).toBeVisible();
    // The planner scrolls the map into view by itself once a plan arrives; let that finish before scrolling down.
    await page.waitForTimeout(1500);
    // At the peek, raised to half (a tap on the handle) and full (End): the page grows with the sheet.
    for (const snap of ["peek", "half", "full"] as const) {
      if (snap === "half") {
        await page.locator("[data-plan-sheet-handle]").click();
        await expect(page.locator("[data-plan-sheet]")).toHaveAttribute("data-snap", "half");
      }
      if (snap === "full") {
        await page.locator("[data-plan-sheet-handle]").press("End");
        await expect(page.locator("[data-plan-sheet]")).toHaveAttribute("data-snap", "full");
      }
      await page.evaluate(() => window.scrollTo({ top: document.documentElement.scrollHeight, behavior: "instant" }));
      await page.waitForTimeout(500);
      const res = await page.evaluate(() => {
        const sheet = document.querySelector("[data-plan-sheet]")!.getBoundingClientRect();
        const credit = document.querySelector("[data-overnight-suggestions] [data-ridb-credit]")!.getBoundingClientRect();
        const warn = document.querySelector("[data-overnight-suggestions] [data-stay-warning]")!.getBoundingClientRect();
        return { sheetTop: sheet.top, creditBottom: credit.bottom, warnBottom: warn.bottom };
      });
      expect(res.creditBottom, `sheet at ${snap}`).toBeLessThanOrEqual(res.sheetTop);
      expect(res.warnBottom, `sheet at ${snap}`).toBeLessThanOrEqual(res.sheetTop);
    }
    await ctx.close();
  });
}
