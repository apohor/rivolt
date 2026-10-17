import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// ui_theme_next on Activity and Money: before/after (dark), flag-off check, axe.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const PAGES = [
  { name: "activity", url: "/activity", ready: "Activity" },
  { name: "money", url: "/money", ready: "Money" },
] as const;
const VIEWS = [
  { name: "iphone", vp: { width: 390, height: 844 } },
  { name: "desktop", vp: { width: 1280, height: 900 } },
] as const;

for (const pg of PAGES) {
  for (const v of VIEWS) {
    for (const state of ["off", "next"] as const) {
      test(`ui ${state}: ${pg.name} · ${v.name} · dark`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
        const page = await ctx.newPage();
        await page.addInitScript((s) => {
          localStorage.setItem("rivolt.ui.next", s === "next" ? "on" : "off");
          localStorage.setItem("rivolt.theme", "dark");
        }, state);
        await stubApi(page);
        await page.goto(pg.url);
        await expect(page.getByRole("heading", { name: pg.ready }).first()).toBeVisible();
        await page.waitForTimeout(900);
        if (state === "off") await expect(page.locator("html")).not.toHaveAttribute("data-ui", "next");
        await page.screenshot({ path: path.join(SHOTS, `uinext-${pg.name}-${v.name}-${state}.png`) });
        if (state === "next") {
          const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
          const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
          expect(summary, summary.join("\n")).toEqual([]);
        }
        await ctx.close();
      });
    }
  }
}
