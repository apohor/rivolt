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
    await open(page, (m) => ({ month: m, currency: "USD", could_have_saved: 12.4, sessions_moved: 9, sessions_left_out: 3, left_out_reasons: { no_cheaper_hours: 2, plugged_in_too_briefly: 1 }, sessions_considered: 12, basis: "Cheapest window that fits inside each session.", as_charged_total: 40, if_moved_total: 27.6 }), asked);
    const card = page.locator("[data-savings]");
    expect(asked).toContain(LAST);
    await expect(card).toContainText("Moving 9 home charges into your off-peak windows could have saved about $12.40 (estimate).");
    await expect(card).toContainText("3 sessions could not be moved and are left out.");
    await expect(card).toContainText("Why: already at the cheapest hours it could use (2); plugged in too briefly for the charge to fit in one stretch (1).");
    await expect(card).toContainText("Based on 12 home charges.");
    await expect(card).toContainText("Cheapest window that fits inside each session.");
    await expect(card).toContainText("as charged $40.00 · if moved $27.60 (estimate)");
    await expect(card).not.toContainText(/overpaid|wasted/i);
    await expect(card).toContainText("Rivolt never changes charging on the truck");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include("[data-savings]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await card.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `savings-${v.w}-${v.scheme}.png`), fullPage: true });
    await page.getByRole("button", { name: "This month so far" }).click();
    await expect.poll(() => asked).toContain(currentMonth(NOW));
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
  await page.reload();
  await expect(card).toContainText("Set your home rate first");
  needs = "more_home_sessions";
  await page.reload();
  await expect(card).toContainText("A few more home charges are needed");
  // The way out for someone with no off-peak hours at all.
  needs = "offpeak_schedule";
  await page.reload();
  await expect(card.locator("[data-savings-flat-link]")).toHaveAttribute("href", /settings\?tab=charging#offpeak/);
  await ctx.close();
});

test("savings: a server without the endpoint shows no card", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page, { scenario: "parked" });
  await page.route("**/api/ledger/monthly**", (r) => r.fulfill({ json: ledgerMonthFixture(currentMonth(NOW), NOW) }));
  await page.route("**/api/ledger/savings**", (r) => r.fulfill({ status: 404, json: { error: "not found" } }));
  await page.goto("/money");
  await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
  await expect(page.locator("[data-savings]")).toHaveCount(0);
  await ctx.close();
});

test("share statement: the off-peak share toggle shows only with a share, and is off every time it opens", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, (m) => ({ month: m, currency: "USD", could_have_saved: 5, sessions_moved: 4, sessions_left_out: 0, offpeak_share_pct: 61.6, offpeak_share_sessions: 9 }), []);
  await page.getByRole("button", { name: "Share statement" }).click();
  const sheet = page.locator("[data-share-sheet]");
  const toggle = sheet.getByLabel(/Include off-peak share/);
  await expect(toggle).not.toBeChecked();
  await expect(sheet).toContainText("No rates, no dollars, no times.");
  await toggle.check();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Share statement" }).click();
  await expect(sheet.getByLabel(/Include off-peak share/)).not.toBeChecked();
  expect((await new AxeBuilder({ page }).include("[data-share-sheet]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
  await ctx.close();
});

test("share statement: no share from the server, no toggle", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, (m) => ({ month: m, currency: "USD", could_have_saved: 5, sessions_moved: 4, sessions_left_out: 0 }), []);
  await page.getByRole("button", { name: "Share statement" }).click();
  await expect(page.locator("[data-share-sheet]").getByLabel(/Include cost/)).toBeVisible();
  await expect(page.locator("[data-share-sheet]").getByLabel(/off-peak/i)).toHaveCount(0);
  await ctx.close();
});

test("flat rate: the card says so with a way back, and the share sheet has no off-peak toggle", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, () => ({ version: 1, flat_rate: true, reason: "flat_rate", basis: "You said your electricity is one flat rate, so there are no off-peak hours." }), []);
  const card = page.locator("[data-savings]");
  await expect(card.locator("[data-savings-flat]")).toContainText("one flat rate");
  await expect(card.getByRole("link", { name: "Change in Settings" })).toHaveAttribute("href", /settings\?tab=charging#offpeak/);
  await expect(card).not.toContainText("could have saved");
  await expect(card).not.toContainText("Estimated from");
  await page.getByRole("button", { name: "Share statement" }).click();
  await expect(page.locator("[data-share-sheet]").getByLabel(/off-peak/i)).toHaveCount(0);
  await ctx.close();
});
