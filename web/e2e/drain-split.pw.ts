import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Parked drain: when the loss happened (awake or asleep), as an estimate, with a two-part bar.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`the drain card says when the loss happened · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page, { drain: "ok" });
    await page.goto("/truck");
    const split = page.locator("[data-drain-split='ok']");
    await expect(split).toContainText("Of the 5% your truck lost while parked this week, about 4% was while it was awake");
    await expect(split).toContainText("Estimate: the battery reports whole percents.");
    await expect(split.getByRole("img")).toHaveAttribute("aria-label", /% of the loss was while awake/);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await split.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `drain-split-${v.n}-dark.png`) });
    await ctx.close();
  });
}
