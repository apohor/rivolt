import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { currentMonth, ledgerMonthFixture, shiftMonth } from "../src/lib/ledger";
import { e2eNow } from "./clock";

// Off-peak savings on Money: what moving home charging into the user's off-peak windows could have saved, always an estimate,
// with the left-out sessions named, and plain states when the schedule or the home rate is missing.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const NOW = new Date(e2eNow());
const LAST = shiftMonth(currentMonth(NOW), -1);

async function open(page: Page, answer: (month: string) => object, asked: string[]) {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page, { scenario: "parked" });
  await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ json: ledgerMonthFixture(currentMonth(NOW), NOW) }));
  await page.route("**/api/ledger/savings**", (r) => {
    const m = new URL(r.request().url()).searchParams.get("month") ?? "";
    asked.push(m);
    return r.fulfill({ json: answer(m) });
  });
  await page.goto("/money");
  await expect(page.locator("[data-savings]")).toBeVisible();
}

for (const v of [
  { w: 390, scheme: "dark" as const },
  { w: 1280, scheme: "light" as const },
]) {
  test(`savings: the estimate, the left-out sessions, last month then this month · ${v.w}px ${v.scheme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: v.w, height: 900 }, colorScheme: v.scheme });
    const page = await ctx.newPage();
    const asked: string[] = [];
    await open(page, (m) => ({ month: m, currency: "USD", could_have_saved: 12.4, sessions_moved: 9, sessions_left_out: 3, reasons: { no_window_fits: 2, too_short: 1 }, as_charged_total: 40, if_moved_total: 27.6 }), asked);
    const card = page.locator("[data-savings]");
    expect(asked[0]).toBe(LAST);
    await expect(card).toContainText("Moving 9 home charges into your off-peak windows could have saved about $12.40 (estimate).");
    await expect(card).toContainText("3 sessions could not be moved and are left out.");
    await expect(card).toContainText("Why: 2 no window fits, 1 too short.");
    await expect(card).toContainText("as charged $40.00 · if moved $27.60 (estimate)");
    await expect(card).not.toContainText(/overpaid|wasted/i);
    await expect(card).toContainText("Rivolt never changes charging on the truck");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include("[data-savings]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await card.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `savings-${v.w}-${v.scheme}.png`), fullPage: true });
    await page.getByRole("button", { name: "This month so far" }).click();
    await expect.poll(() => asked.at(-1)).toBe(currentMonth(NOW));
    await ctx.close();
  });
}

test("savings: states with no number say what is missing and link to it", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  let needs = "offpeak_schedule";
  await open(page, () => ({ needs }), []);
  const card = page.locator("[data-savings]");
  await expect(card).toContainText("Add your off-peak hours and rate");
  await expect(card.getByRole("link", { name: "Set off-peak hours" })).toHaveAttribute("href", /settings\?tab=charging#offpeak/);
  await expect(card).not.toContainText("$");
  needs = "home_rate";
  await page.getByRole("button", { name: "This month so far" }).click();
  await expect(card).toContainText("Set your home rate first");
  needs = "more_sessions";
  await page.reload();
  await expect(card).toContainText("A few more home charges are needed");
  await ctx.close();
});

test("savings: a server without the endpoint shows no card", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page, { scenario: "parked" });
  await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ json: ledgerMonthFixture(currentMonth(NOW), NOW) }));
  await page.route("**/api/ledger/savings**", (r) => r.fulfill({ status: 404, json: { error: "not found" } }));
  await page.goto("/money");
  await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
  await expect(page.locator("[data-savings]")).toHaveCount(0);
  await ctx.close();
});
