import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { currentMonth, ledgerMonthFixture } from "../src/lib/ledger";
import { e2eNow } from "./clock";

// A charge whose energy was lost and worked out from the battery's change: its kWh and cost are marked as estimates on Money's receipts,
// and a month that includes one says so under the energy total.
const NOW = new Date(e2eNow());

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`money: an estimated-energy receipt is marked, and the month says so · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page, { scenario: "parked" });
    const m = ledgerMonthFixture(currentMonth(NOW), NOW);
    const target = m.receipts.find((r) => r.cost != null && r.cost > 0 && r.cost_source !== "unknown") ?? m.receipts[0];
    target.energy_estimated = true;
    for (const s of target.sessions) s.energy_estimated = true;
    m.energy.includes_estimated = true;
    await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ json: m }));
    await page.goto("/money");
    await expect(page.locator("[data-energy-estimated-note]")).toContainText("estimated energy figure");
    await expect(page.locator("[data-energy-estimated]").first()).toBeVisible();
    const row = page.locator("[data-energy-estimated='true']").filter({ hasText: /~\d+(\.\d)? kWh/ }).first();
    await expect(row).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
    expect(res.violations.map((x) => x.id), res.violations.map((x) => x.id).join()).toEqual([]);
    await ctx.close();
  });
}
