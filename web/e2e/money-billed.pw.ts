import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { currentMonth, ledgerMonthFixture } from "../src/lib/ledger";

// Money with Rivian receipts (flag rivian_receipts): a matched session shows what Rivian
// billed with "Billed by Rivian", the rest show "Estimated"; the month total is split the
// same way; the network/charger shows for away sessions and never for home. Without the
// flag (no billed fields from the server) nothing changes.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

function withReceipts(on: boolean) {
  const m = ledgerMonthFixture(currentMonth());
  if (!on) return m;
  const awayIdx = m.receipts.findIndex((r) => r.place === "away" && r.cost_source === "network_estimate");
  m.receipts.forEach((r, i) => {
    r.billed_sessions = i === awayIdx ? r.session_count : 0;
    r.estimated_sessions = i === awayIdx ? 0 : r.session_count;
    if (i === awayIdx) {
      r.billed_cost = 21.4;
      r.billed_currency = m.currency;
      r.billed_vendor = "Electrify America";
      r.billed_charger = "Barstow 12";
      r.sessions.forEach((s) => {
        s.billed_cost = 21.4;
        s.billed_currency = m.currency;
      });
    }
  });
  const billed = 21.4;
  m.spend.billed_total = billed;
  m.spend.estimated_total = Math.max(0, m.spend.total - billed);
  m.billed_sessions = 1;
  m.estimated_sessions = m.receipts.reduce((a, r) => a + (r.estimated_sessions ?? 0), 0);
  return m;
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280, 1440] as const) {
    test(`Money billed vs estimated · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { scenario: "parked" });
      const m = withReceipts(true);
      await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ json: m }));
      await page.goto("/money");
      await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
      await expect(page.locator("[data-billed-split]")).toContainText("billed by Rivian");
      await expect(page.locator("[data-billed-split]")).toContainText("estimated");
      const billed = page.locator("[data-price-label='billed']");
      await expect(billed).toHaveCount(1);
      await expect(page.locator("[data-price-label='estimated']").first()).toBeVisible();
      // The billed row shows the billed amount and where; home rows never show a vendor.
      const row = page.locator("li").filter({ has: billed });
      await expect(row).toContainText("$21.40");
      await expect(row).toContainText("Electrify America");
      await expect(row).toContainText("Barstow 12");
      expect(await page.locator("[data-billed-where]").count()).toBe(1);
      await row.scrollIntoViewIfNeeded();
      await page.waitForTimeout(500);
      await page.screenshot({ path: path.join(SHOTS, `money-billed-${w}-${theme}.png`) });
      const res = await page.evaluate(() => ({
        overflow: document.documentElement.scrollWidth > window.innerWidth + 1,
        wideHero: (() => {
          const el = document.querySelector("[data-billed-split]") as HTMLElement | null;
          return el ? el.getBoundingClientRect().right > window.innerWidth : false;
        })(),
      }));
      expect(res.overflow).toBe(false);
      expect(res.wideHero).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

for (const ui of ["off", "on"] as const) {
  test(`without the flag nothing changes · ui ${ui}`, async ({ page }) => {
    await page.addInitScript((s) => localStorage.setItem("rivolt.ui.next", s), ui);
    await stubApi(page, { scenario: "parked" });
    const m = withReceipts(false);
    await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ json: m }));
    await page.goto("/money");
    await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
    await expect(page.locator("[data-price-label], [data-billed-split], [data-billed-where]")).toHaveCount(0);
    await expect(page.getByText("Billed by Rivian")).toHaveCount(0);
  });
}
