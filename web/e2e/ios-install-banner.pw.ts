import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The iPhone "add to home screen" banner shows once, on Now: not on the other screens, and not on the next visit.
const IPHONE = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1";

test("shown once on Now, not on other screens, and not on the next visit", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark", userAgent: IPHONE });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  const banner = page.getByText(/Add to Home Screen/);
  await page.goto("/");
  await expect(banner).toBeVisible();
  // Another screen has no banner; back on Now, in the same visit, it is still there until hidden.
  await page.getByRole("link", { name: "Activity" }).click();
  await expect(page).toHaveURL(/\/activity/);
  await expect(banner).toHaveCount(0);
  await page.getByRole("link", { name: "Now" }).click();
  await expect(banner).toBeVisible();
  // The next visit does not show it.
  await page.reload();
  await expect(page.getByRole("heading", { name: /Now|Rivolt/ }).first()).toBeVisible().catch(() => undefined);
  await page.waitForTimeout(800);
  await expect(banner).toHaveCount(0);
  await ctx.close();
});

test("a browser that is not an iPhone never shows it", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  await page.goto("/");
  await page.waitForTimeout(800);
  await expect(page.getByText(/Add to Home Screen/)).toHaveCount(0);
  await ctx.close();
});
