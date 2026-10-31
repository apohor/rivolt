import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// What a free account sees where the server answers HTTP 402 (outside_free_window, pro_required): a note that says why and that nothing is
// lost, never an error box or "not found". The server's answer is stubbed; nothing here asks for billing.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const FREE_ME = { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, plan: "free", plan_source: null, plan_period_end: null, plan_trial_ends: null, plan_ended_at: null, plan_ended_source: null, billing_enabled: true, history_days: 90 };
const json = (body: unknown, status: number) => ({ status, contentType: "application/json", body: JSON.stringify(body) });

async function asFree(page: Page) {
  await stubApi(page);
  await page.route("**/api/auth/me", (r) => r.fulfill(json(FREE_ME, 200)));
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`a drive and a charge older than the free history say so · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await asFree(page);
    await page.route("**/api/drives/old-drive", (r) => r.fulfill(json({ error: "outside_free_window", free_days: 90 }, 402)));
    await page.route("**/api/charges/old-charge", (r) => r.fulfill(json({ error: "outside_free_window", free_days: 90 }, 402)));
    await page.goto("/drives/old-drive");
    await expect(page.locator("[data-pro-limit=outside_free_window]")).toContainText("older than the last 90 days");
    await expect(page.getByText("Drive not found")).toHaveCount(0);
    await page.goto("/charges/old-charge");
    const note = page.locator("[data-pro-limit=outside_free_window]");
    await expect(note).toContainText("kept, and it is in your export");
    await note.getByRole("button", { name: "See Pro" }).click();
    await expect(page.locator("[data-pro-sheet]")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: path.join(SHOTS, `pro-limit-detail-${v.n}-dark.png`) });
    await ctx.close();
  });

  test(`Money: a month outside the window, and its CSV · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await asFree(page);
    await page.route("**/api/ledger/monthly?*month=2020-01*", (r) => r.fulfill(json({ error: "outside_free_window", free_days: 90 }, 402)));
    await page.goto("/money?month=2020-01");
    await expect(page.locator("[data-pro-limit=outside_free_window]")).toBeVisible();
    await page.getByRole("button", { name: "Back to this month" }).click();
    await expect(page.locator("[data-money-headline]")).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS, `pro-limit-money-${v.n}-dark.png`) });
    await ctx.close();
  });

  test(`Truck: trends, range at a temperature and battery capacity are Pro notes, not errors · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await asFree(page);
    const pro = json({ error: "pro_required" }, 402);
    await page.route(`**/api/vehicles/${VEHICLE_ID}/trends*`, (r) => r.fulfill(pro));
    await page.route(`**/api/vehicles/${VEHICLE_ID}/cold-range*`, (r) => r.fulfill(pro));
    await page.route(`**/api/vehicles/${VEHICLE_ID}/pack-health`, (r) => r.fulfill(pro));
    await page.goto("/truck");
    // Battery capacity, Trends and Range at a temperature: one note each.
    await expect(page.locator("[data-pro-limit=pro_required]")).toHaveCount(3);
    await expect(page.locator("[data-pro-limit=pro_required]").first()).toContainText("part of Rivolt Pro");
    await expect(page.getByText("Couldn't load trends")).toHaveCount(0);
    await expect(page.getByText("Couldn't load battery capacity")).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `pro-limit-truck-${v.n}-dark.png`), fullPage: true });
    await ctx.close();
  });
}
