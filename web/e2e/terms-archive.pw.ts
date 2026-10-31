import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// /terms/2026-10-23: the frozen October 23 Terms, public and signed out, next to the current /terms.
test("the October 23 Terms are public at their own address", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.goto("/terms/2026-10-23");
  await expect(page.getByRole("heading", { level: 1, name: "Rivolt Terms of Service" })).toBeVisible();
  await expect(page.getByText("Last updated: October 23, 2026")).toBeVisible();
  await expect(page.locator("[data-terms-archive]")).toContainText("It does not change");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.goto("/terms");
  await expect(page.locator("[data-terms-archive]")).toHaveCount(0);
});
