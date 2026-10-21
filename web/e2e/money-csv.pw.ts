import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { currentMonth, ledgerMonthFixture } from "../src/lib/ledger";
import { e2eNow } from "./clock";

// Money's "Download CSV": a plain same-origin download of the month, in the page's time zone; nothing is requested until it is clicked.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
const NOW = new Date(e2eNow());

for (const v of [
  { w: 390, scheme: "dark" as const },
  { w: 1280, scheme: "light" as const },
]) {
  test(`download CSV: the link, the request and the file · ${v.w}px ${v.scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: v.w, height: 900 }, colorScheme: v.scheme, acceptDownloads: true, timezoneId: "America/Chicago" });
    const page = await ctx.newPage();
    await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
    await stubApi(page, { scenario: "parked" });
    await page.route("**/api/ledger/monthly?**", (r) => r.fulfill({ json: ledgerMonthFixture(currentMonth(NOW), NOW) }));
    const csvReqs: string[] = [];
    await page.route("**/api/ledger/monthly.csv**", (r) => {
      csvReqs.push(r.request().url());
      return r.fulfill({
        status: 200,
        headers: { "content-type": "text/csv; charset=utf-8", "content-disposition": `attachment; filename="rivolt-statement-${currentMonth(NOW)}.csv"` },
        body: "section,label,value\ntotal,spend,92.79\n",
      });
    });
    await page.goto("/money");
    await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
    const link = page.locator("[data-csv-download]");
    await expect(link).toBeVisible();
    await expect(link).toHaveText("Download CSV");
    // Nothing is requested until it is clicked (the download itself is the browser's, not a script's).
    expect(csvReqs).toEqual([]);
    const href = (await link.getAttribute("href")) ?? "";
    expect(href).toContain("/api/ledger/monthly.csv?");
    expect(href).toContain(`month=${currentMonth(NOW)}`);
    expect(href).toContain("tz=America%2FChicago");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include("[data-csv-download]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `money-csv-${v.w}-${v.scheme}.png`) });
    const download = page.waitForEvent("download");
    await link.click();
    const file = await download;
    expect(file.suggestedFilename()).toBe(`rivolt-statement-${currentMonth(NOW)}.csv`);
    await ctx.close();
  });
}

test("download CSV: a month with nothing to show has no live link", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page, { scenario: "parked" });
  const empty = ledgerMonthFixture(currentMonth(NOW), NOW);
  empty.energy.sessions = 0;
  empty.energy.kwh_added = 0;
  empty.receipts = [];
  empty.spend.total = 0;
  await page.route("**/api/ledger/monthly?**", (r) => r.fulfill({ json: empty }));
  await page.goto("/money");
  await expect(page.locator("[data-csv-download]")).toHaveCount(0);
  await ctx.close();
});
