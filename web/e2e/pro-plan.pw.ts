import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The Pro screens on a server that bills (mocked: billing is off in production). Settings → Account → Plan, the Pro sheet with its
// price, inclusions, unticked consent and Terms/Privacy links, and the lapse notice. Nothing is purchased: the stub answers no checkout.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function withPlan(page: Page, plan: Record<string, unknown>) {
  await stubApi(page);
  await page.route("**/api/auth/me", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, ...plan }) }),
  );
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`free account: Plan card and the Pro sheet · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await withPlan(page, { plan: "free", plan_source: null, plan_period_end: null, plan_trial_ends: null, plan_ended_at: null, plan_ended_source: null, billing_enabled: true, history_days: 90 });
    await page.goto("/settings#plan");
    const card = page.locator("[data-plan-card]");
    await expect(card).toHaveAttribute("data-plan-card", "free");
    await page.screenshot({ path: path.join(SHOTS, `pro-plan-card-${v.n}-dark.png`) });
    await card.getByRole("button", { name: "See Pro" }).click();
    const sheet = page.locator("[data-pro-sheet]");
    await expect(sheet).toContainText("$3.99");
    await expect(sheet).toContainText("$34.99");
    await expect(sheet.getByRole("radio", { name: /Yearly/ })).not.toBeChecked();
    await expect(sheet.getByRole("button", { name: "Choose a plan" })).toBeDisabled();
    await sheet.getByText("Yearly").click();
    await expect(sheet.getByRole("checkbox")).not.toBeChecked();
    await expect(sheet.getByRole("button", { name: /Subscribe: \$34\.99 a year, renews yearly/ })).toBeDisabled();
    await expect(sheet).toContainText("the first charge is today");
    await expect(sheet.getByRole("link", { name: "Terms" })).toBeVisible();
    await expect(sheet.getByRole("link", { name: "Privacy" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `pro-sheet-${v.n}-dark.png`) });
    await ctx.close();
  });

  test(`lapsed account: the notice says nothing is deleted · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await withPlan(page, { plan: "free", plan_source: null, plan_ended_at: "2026-10-01T00:00:00Z", plan_ended_source: "stripe", billing_enabled: true, history_days: 90 });
    await page.goto("/");
    const note = page.locator("[data-plan-notice]");
    await expect(note).toContainText("Nothing is deleted");
    await expect(note.getByRole("link", { name: "Export everything" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: path.join(SHOTS, `pro-lapse-${v.n}-dark.png`) });
    await ctx.close();
  });
}

test("billing off: no plan screen anywhere", async ({ page }) => {
  await stubApi(page);
  await page.goto("/settings");
  await expect(page.locator("[data-plan-card]")).toHaveCount(0);
  await page.goto("/");
  await expect(page.locator("[data-plan-notice]")).toHaveCount(0);
});
