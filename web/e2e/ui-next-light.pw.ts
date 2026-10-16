import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// ui_theme_next, light theme: before/after (flag off vs on), flag-off check, axe.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const PAGES = [
  { name: "now-charging", url: "/", scenario: "charging", ready: (p: import("@playwright/test").Page) => expect(p.getByRole("region", { name: "Now" })).toBeVisible() },
  { name: "truck", url: "/truck", scenario: "parked", ready: (p: import("@playwright/test").Page) => expect(p.getByText("Pack capacity")).toBeVisible() },
  { name: "activity", url: "/activity", scenario: "parked", ready: (p: import("@playwright/test").Page) => expect(p.getByRole("heading", { name: "Activity" }).first()).toBeVisible() },
] as const;

for (const pg of PAGES) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    for (const state of ["off", "on"] as const) {
      test(`light ${state}: ${pg.name} · ${v.name}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "light" });
        const page = await ctx.newPage();
        await page.addInitScript((s) => {
          localStorage.setItem("rivolt.ui.next", s);
          localStorage.setItem("rivolt.theme", "light");
        }, state);
        await stubApi(page, { scenario: pg.scenario, drain: "ok" });
        await page.goto(pg.url);
        await pg.ready(page);
        await page.waitForTimeout(800);
        await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
        if (state === "off") await expect(page.locator("html")).not.toHaveAttribute("data-ui", "next");
        await page.screenshot({ path: path.join(SHOTS, `uinext-light-${pg.name}-${v.name}-${state === "on" ? "next" : "off"}.png`) });
        if (state === "on") {
          const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
          const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
          expect(summary, summary.join("\n")).toEqual([]);
        }
        await ctx.close();
      });
    }
  }
}
