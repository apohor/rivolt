import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The operator-transfer notice from the server (GET /api/auth/me "notice"): a dismissible
// line on every signed-in page, and a line on Settings → Account that stays.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const NOTICE = {
  id: "transfer-2026-12-01",
  text: "From December 1, 2026, Rivolt will be run by ZeroDowntime LLC. Nothing changes for your data; details in the email we sent, or export or delete your data in Settings → Data.",
  starts_at: "2026-10-01T00:00:00Z",
  until: "2099-12-01T00:00:00Z",
  link_text: "Settings → Data",
  link_url: "/settings?tab=data",
};

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  for (const scheme of ["dark", "light"] as const) {
    test(`site notice banner and settings line · ${v.n} ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await stubApi(page);
      await page.route("**/api/auth/me", (r) =>
        r.fulfill({ json: { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, notice: NOTICE } }),
      );
      await page.goto("/");
      const banner = page.getByRole("region", { name: "Notice" });
      await expect(banner).toContainText("From December 1, 2026, Rivolt will be run by ZeroDowntime LLC.");
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      expect((await new AxeBuilder({ page }).include("[data-site-notice]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `site-notice-${v.n}-${scheme}.png`), fullPage: false });
      await banner.getByRole("button", { name: "Dismiss notice" }).click();
      await expect(banner).toHaveCount(0);
      await page.goto("/settings?tab=account");
      await expect(page.getByRole("region", { name: "Notice" })).toHaveCount(0);
      await expect(page.locator("[data-site-notice-line]")).toContainText("ZeroDowntime LLC");
      await page.screenshot({ path: path.join(SHOTS, `site-notice-settings-${v.n}-${scheme}.png`), fullPage: false });
      await ctx.close();
    });
  }
}
