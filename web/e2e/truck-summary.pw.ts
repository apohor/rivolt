import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The line at the top of Truck: the pack's usable capacity and the 7-day parked drain, as the
// cards below show them; each half shows its card.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  for (const scheme of ["dark", "light"] as const) {
    test(`truck summary line · ${v.n} ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await stubApi(page, { drain: "ok", uiNext: true });
      await page.goto("/truck");
      const line = page.getByRole("region", { name: "Truck summary" });
      await expect(line).toContainText(/^Pack (about )?\d+(\.\d)? kWh usable/);
      await expect(line).toContainText(/parked drain [\d.]+% a day \(last 7 days\)/);
      // The figures are the cards' own.
      const packFig = (await page.locator("#truck-pack").innerText()).match(/(\d+(?:\.\d)?) kWh/)![1];
      await expect(line).toContainText(`${packFig} kWh usable`);
      for (const b of await line.getByRole("button").all()) expect((await b.boundingBox())!.height).toBeGreaterThanOrEqual(44);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      expect((await new AxeBuilder({ page }).include("[data-truck-summary]").analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `truck-summary-${v.n}-${scheme}.png`), fullPage: false });
      await line.getByRole("button", { name: /^Pack/ }).click();
      await expect(page.locator("#truck-pack")).toBeInViewport();
      await ctx.close();
    });
  }
}
