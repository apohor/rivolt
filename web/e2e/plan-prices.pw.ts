import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { DEMO_PLAN, stubApi } from "./stub";

// The planner's fast-charging cost: a range from each stop's price, the price's own label, "—" and "Price shown at the
// charger." for a stop with no price (never $0), partial totals say so, and an older saved plan is marked.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const OBSERVED = { basis: "observed", per_kwh_low: 0.6, per_kwh_high: 0.6, source: "DCFC Tracker", source_url: "https://dcfctracker.com/reports/electrify-america", observed_month: "2026-10", label: "Typical price seen by DCFC Tracker, Oct 2026. The price at the charger may differ." };
const TESLA = { basis: "observed", per_kwh_low: 0.45, per_kwh_high: 0.6, low_note: "with the Supercharging Membership", high_note: "without the membership", source: "DCFC Tracker", source_url: "https://dcfctracker.com/reports/tesla", observed_month: "2026-10", label: OBSERVED.label };
const base = DEMO_PLAN.costs[0];
const stop = (price: unknown, low: number | null, high: number | null) => ({ ...base.breakdown[0], price, cost_low: low, cost_high: high });

async function open(page: Page, next: boolean, cost: Record<string, unknown>) {
  if (next) await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page, { demo: true, signedIn: false });
  await page.route(/\/api\/demo\/trips\/[a-z0-9-]+\/plan$/, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...DEMO_PLAN, costs: [cost] }) }));
  await page.goto("/demo/trips/plan");
  await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
}

const body = async (page: Page) => (await page.locator("body").innerText()).replace(/\s+/g, " ");

for (const next of [true, false]) {
  const look = next ? "new look" : "classic";

  test(`a priced stop: range, the tracker label with its link, one note · ${look}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: next ? { width: 1280, height: 900 } : { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, next, { ...base, dcfc_spend_low: 18.4, dcfc_spend_high: 22.9, breakdown: [stop(TESLA, 18.4, 22.9)] });
    await expect(page.locator("[data-cost-note]")).toHaveText("estimate (typical observed price)");
    const text = await body(page);
    expect(text).toMatch(/\$18\.40–\$22\.90/);
    expect(text).not.toMatch(/with memberships|save another|\$0\.00|network rate|official|listed/i);
    if (next) {
      await page.getByRole("button", { name: /Sample Fast Charge/ }).first().click();
    }
    const note = page.locator("[data-price-note='observed']").first();
    await expect(note).toContainText("Typical price seen by DCFC Tracker, Oct 2026. The price at the charger may differ.");
    await expect(note.getByRole("link", { name: "DCFC Tracker" })).toHaveAttribute("href", "https://dcfctracker.com/reports/tesla");
    await expect(note).toContainText("$18.40 with the Supercharging Membership · $22.90 without the membership");
    expect((await new AxeBuilder({ page }).include("[data-price-note]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: path.join(SHOTS, `plan-price-${next ? "next" : "classic"}-iphone-dark.png`) });
    await ctx.close();
  });

  test(`no price: a dash and "Price shown at the charger.", never $0 · ${look}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: next ? { width: 1280, height: 900 } : { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, next, { ...base, dcfc_spend_low: 0, dcfc_spend_high: 0, priced_stops: 0, unpriced_stops: 1, estimate_basis: "none", breakdown: [stop(null, null, null)] });
    if (next) await page.getByRole("button", { name: /Sample Fast Charge/ }).first().click();
    await expect(page.locator("[data-price-note='none']").first()).toHaveText("Price shown at the charger.");
    const text = await body(page);
    expect(text).not.toMatch(/\$0\.00|\$0(?![.\d])/);
    await ctx.close();
  });

  test(`a partial total says how many stops are priced · ${look}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: next ? { width: 1280, height: 900 } : { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, next, { ...base, dcfc_spend_low: 14.4, dcfc_spend_high: 14.4, priced_stops: 1, unpriced_stops: 2, estimate_basis: "observed", breakdown: [stop(OBSERVED, 14.4, 14.4)] });
    await expect(page.locator("[data-cost-note]")).toContainText("1 of 3 stops priced; the rest show their price at the charger");
    await ctx.close();
  });

  test(`an older saved plan is marked, with the interim note · ${look}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: next ? { width: 1280, height: 900 } : { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    const { breakdown: _b, dcfc_spend_low: _l, dcfc_spend_high: _h, priced_stops: _p, unpriced_stops: _u, estimate_basis: _e, ...old } = base as Record<string, unknown>;
    void _b; void _l; void _h; void _p; void _u; void _e;
    await open(page, next, { ...old, dcfc_spend: 14.4 });
    await expect(page.locator("[data-cost-note]")).toHaveText("Estimate, price varies by site (rates as of mid-2025)");
    expect(await body(page)).toMatch(/\$14\.40/);
    await ctx.close();
  });

  test(`your own rate says "estimate" without the observed wording · ${look}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: next ? { width: 1280, height: 900 } : { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, next, { ...base, estimate_basis: "your_setting", breakdown: [stop({ basis: "your_setting", per_kwh_low: 0.6, per_kwh_high: 0.6, label: "Your saved rate" }, 14.4, 14.4)] });
    await expect(page.locator("[data-cost-note]")).toHaveText("estimate");
    await ctx.close();
  });
}
