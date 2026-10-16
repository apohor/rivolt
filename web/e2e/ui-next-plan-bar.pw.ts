import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// ui_theme_next planner route bar (signed in): From/To rows with a swap, departure
// chips, the start SoC slider, and the rest under More options.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { n: "iphone", vp: { width: 390, height: 844 } },
    { n: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    test(`route bar · ${v.n} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page);
      await page.goto("/trips/plan");
      const bar = page.locator("[data-plan-bar]");
      await expect(bar.locator("[data-plan-field]")).toHaveCount(2);
      await expect(bar.getByRole("button", { name: "Now" })).toHaveAttribute("aria-pressed", "true");
      // Pick the Home preset for From, then swap: To takes it.
      await bar.locator("[data-plan-field][data-heading='From']").getByRole("button", { name: /Home/ }).click();
      await expect(bar.locator("[data-plan-field][data-heading='From']")).toContainText("Home");
      await bar.getByRole("button", { name: "Swap From and To" }).click();
      await expect(bar.locator("[data-plan-field][data-heading='To']")).toContainText("Home");
      await page.waitForTimeout(500);
      await page.screenshot({ path: path.join(SHOTS, `bar-${v.n}-${theme}-after.png`) });
      // More options: closed by default, drive mode lives inside.
      const toggle = bar.locator("[data-plan-more-toggle]");
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await expect(page.getByRole("radiogroup", { name: "Drive mode" })).toBeHidden();
      await toggle.click();
      await expect(page.getByRole("radiogroup", { name: "Drive mode" })).toBeVisible();
      await page.screenshot({ path: path.join(SHOTS, `bar-${v.n}-${theme}-more.png`) });
      const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
      const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}
