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
    await expect(section.getByRole("button", { name: "Not this trip" })).toBeVisible();
    await expect(section.getByText(/Likely match/)).toBeVisible();
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
