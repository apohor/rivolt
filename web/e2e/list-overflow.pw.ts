import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The Drives and Charges headers (view toggle + window picker) wrap on a phone instead of pushing the page sideways.
for (const next of [true, false]) {
  for (const path of ["/drives", "/charges"]) {
    test(`${path} has no sideways scroll at 390 · ${next ? "new look" : "classic"}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
      const page = await ctx.newPage();
      if (next) await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
      await stubApi(page);
      await page.goto(path);
      await expect(page.getByRole("button", { name: "All" }).first()).toBeVisible();
      await page.waitForTimeout(500);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await ctx.close();
    });
  }
}
