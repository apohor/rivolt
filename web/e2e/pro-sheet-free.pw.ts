import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// A free account's Pro sheet: no trial anywhere, the 14-day refund wording, and plain "see it in the demo" links.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
const json = (body: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
const FREE_ME = { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, plan: "free", plan_source: null, plan_period_end: null, plan_trial_ends: null, plan_cancels_at: null, plan_grace_ends: null, plan_ended_at: null, plan_ended_source: null, billing_enabled: true, history_days: 90 };

for (const scheme of ["dark", "light"] as const) {
  for (const v of [
    { n: "iphone", vp: { width: 390, height: 844 } },
    { n: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    test(`the free account's Pro sheet · ${v.n} ${scheme}`, async ({ browser }) => {
      test.setTimeout(90_000);
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await stubApi(page);
      await page.route("**/api/auth/me", (r) => r.fulfill(json(FREE_ME)));
      await page.goto("/settings#plan");
      await page.getByRole("button", { name: "See Pro" }).first().click();
      const sheet = page.locator("[data-pro-sheet]");
      await expect(sheet).toBeVisible();
      await sheet.getByLabel(/Yearly/).check({ force: true });
      await sheet.getByRole("checkbox").scrollIntoViewIfNeeded();
      const text = (await sheet.textContent()) ?? "";
      expect(text).not.toMatch(/trial|14 days of Pro|Keep Pro/i);
      expect(text).toContain("14-day refund");
      const links = sheet.locator("[data-demo-links] a");
      await expect(links).toHaveCount(3);
      await expect(links.first()).toHaveAttribute("href", "https://demo.rivolt.dev/demo/trips/plan");
      expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `pro-sheet-free-${v.n}-${scheme}.png`) });
      await ctx.close();
    });
  }
}
