import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Plan vs actual in the one look: a trip not driven yet and a trip still in progress each say so in a plain sentence, and a drive waiting
// for a Confirm / Not this trip is never tucked behind "Show more".
for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`plan vs actual states: not driven, in progress, and a suggestion to confirm · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.goto("/trips/plan?tab=pva");
    const section = page.locator("[data-pva-next]");
    await expect(section).toBeVisible();
    const lines = section.locator("[data-pva-line]");
    await expect(lines).toHaveCount(2);
    await expect(section.getByText("We'll compare once you've driven it.")).toBeVisible();
    await expect(section.getByText("Trip in progress. We'll compare when you're home.")).toBeVisible();
    // The suggestion is shown first, with its two answers and how sure we are.
    await expect(section.getByText(/Was this your trip/)).toBeVisible();
    await expect(section.getByRole("button", { name: "Confirm" })).toBeVisible();
    await expect(section.getByRole("button", { name: "Not this trip", exact: true })).toBeVisible();
    await expect(section.getByText(/Likely match/)).toBeVisible();
    // Two reasons read "Why: <first> Also: <second>", never "Mostly because You ..." (the reasons are full sentences).
    const reasons = section.locator("[data-pva-reasons]").filter({ hasText: "Also:" }).first();
    await expect(reasons).toContainText(/^Why: /);
    await expect(section).not.toContainText("Mostly because");
    // An estimated actual says why its bar is dotted.
    await expect(section.locator("[data-pva-estimated]").first()).toContainText("network rate, not billed");
    // No bare labels where the sentences belong.
    await expect(section.getByText(/^(Not driven yet|In progress|Link lost)$/)).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const res = await new AxeBuilder({ page }).include("[data-pva-next]").withTags(["wcag2a", "wcag2aa"]).analyze();
    expect(res.violations.map((x) => x.id), res.violations.map((x) => x.id).join()).toEqual([]);
    await ctx.close();
  });
}

// No saved trips: the Plan vs actual tab says what it is for, in the Saved tab's tone, instead of a blank screen. Refs #989.
test("plan vs actual with no saved trips shows an empty-state line", async ({ page }) => {
  await stubApi(page);
  await page.route("**/api/trips/saved", (r) => r.fulfill({ status: 200, contentType: "application/json", body: "[]" }));
  await page.goto("/trips/plan?tab=pva");
  const empty = page.locator("[data-pva-empty]");
  await expect(empty).toBeVisible();
  await expect(empty).toContainText("Nothing to compare yet. Plan a route and save it, drive it,");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

// Every comparison failing to load says so with a retry; an older server without the endpoint says that. Neither is a titled card with nothing in it. Refs #1648.
test("plan vs actual: a failing comparison shows an error with a retry; an absent endpoint says so", async ({ page }) => {
  await stubApi(page);
  let status = 500;
  await page.route("**/api/trips/saved/*/actual", (r) => r.fulfill({ status, contentType: "text/plain", body: status === 500 ? "boom" : "not found" }));
  await page.goto("/trips/plan?tab=pva");
  const err = page.locator("[data-pva-error]");
  await expect(err).toHaveText("Couldn't load the comparison. Try again in a moment.");
  status = 404;
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.locator("[data-pva-unavailable]")).toHaveText("Plan vs actual isn't available on this server.");
  await expect(err).toHaveCount(0);
});
