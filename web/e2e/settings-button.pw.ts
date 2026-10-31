import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The Settings button at the top right of every screen (owner decision): a 44 px target named "Settings", active on /settings.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const scheme of ["dark", "light"] as const) {
  for (const v of [
    { n: "iphone", vp: { width: 390, height: 844 } },
    { n: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    test(`the Settings button sits top right on every screen and opens Settings · ${v.n} ${scheme}`, async ({ browser }) => {
      test.setTimeout(90_000);
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await stubApi(page);
      await page.goto("/");
      const button = page.locator("header").getByRole("link", { name: "Settings" });
      await expect(button).toBeVisible();
      const box = (await button.boundingBox())!;
      expect(box.width).toBeGreaterThanOrEqual(44);
      expect(box.height).toBeGreaterThanOrEqual(44);
      // Top right: the right edge is within the page gutter, and it is in the header band.
      expect(box.x + box.width).toBeGreaterThan(v.vp.width - 80);
      expect(box.y).toBeLessThan(80);
      await page.screenshot({ path: path.join(SHOTS, `settings-button-${v.n}-${scheme}.png`) });
      for (const p of ["/activity", "/money", "/truck"]) {
        await page.goto(p);
        await expect(page.locator("header").getByRole("link", { name: "Settings" })).toBeVisible();
      }
      await page.locator("header").getByRole("link", { name: "Settings" }).click();
      await expect(page).toHaveURL(/\/settings/);
      await expect(page.locator("header").getByRole("link", { name: "Settings" })).toHaveAttribute("aria-current", "page");
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `settings-button-active-${v.n}-${scheme}.png`) });
      await ctx.close();
    });
  }
}
