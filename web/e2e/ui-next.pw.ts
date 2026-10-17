import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// ui_theme_next prototype: before/after pairs of Now and Truck (dark), and
// proof that flag off leaves html[data-ui] unset.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const VIEWS = [
  { name: "iphone", vp: { width: 390, height: 844 } },
  { name: "desktop", vp: { width: 1280, height: 900 } },
] as const;

for (const v of VIEWS) {
  for (const state of ["off", "next"] as const) {
    for (const scenario of ["parked", "charging"] as const) {
      test(`ui ${state}: now (${scenario}) · ${v.name} · dark`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
        const page = await ctx.newPage();
        await page.addInitScript((s) => localStorage.setItem("rivolt.ui.next", s === "next" ? "on" : "off"), state);
        await page.addInitScript(() => localStorage.setItem("rivolt.theme", "dark"));
        await stubApi(page, { scenario });
        // A charging session collects a power reading per poll: let three polls pass.
        if (scenario === "charging") await page.clock.install();
        await page.goto("/");
        await expect(page.getByRole("region", { name: "Now" })).toBeVisible();
        if (scenario === "charging") {
          for (let i = 0; i < 4; i++) await page.clock.runFor(16_000);
        }
        await page.waitForTimeout(900);
        if (scenario === "charging" && state === "next") await expect(page.locator(".ui-spark")).toBeVisible();
        if (state === "off") await expect(page.locator("html")).not.toHaveAttribute("data-ui", "next");
        else await expect(page.locator("html")).toHaveAttribute("data-ui", "next");
        await page.screenshot({ path: path.join(SHOTS, `uinext-now-${scenario}-${v.name}-${state}.png`) });
        await ctx.close();
      });
    }
    test(`ui ${state}: truck · ${v.name} · dark`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
      const page = await ctx.newPage();
      await page.addInitScript((s) => localStorage.setItem("rivolt.ui.next", s === "next" ? "on" : "off"), state);
      await page.addInitScript(() => localStorage.setItem("rivolt.theme", "dark"));
      await stubApi(page, { drain: "ok" });
      await page.goto("/truck");
      await expect(page.getByText("Pack capacity")).toBeVisible();
      await page.waitForTimeout(900);
      await page.screenshot({ path: path.join(SHOTS, `uinext-truck-${v.name}-${state}.png`), fullPage: v.name === "iphone" });
      await ctx.close();
    });
  }
}

// The prototype must keep WCAG AA: axe on Now (charging, with the glow and
// gauge) and Truck, dark, with the layer on.
for (const [name, url, scenario] of [
  ["now-charging", "/", "charging"],
  ["now-parked", "/", "parked"],
  ["truck", "/truck", "parked"],
] as const) {
  test(`ui next stays axe-clean: ${name} · iphone · dark`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await page.addInitScript(() => {
      localStorage.setItem("rivolt.ui.next", "on");
      localStorage.setItem("rivolt.theme", "dark");
    });
    await stubApi(page, { scenario, drain: "ok" });
    await page.goto(url);
    await expect(page.locator("html")).toHaveAttribute("data-ui", "next");
    await page.waitForTimeout(1200);
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = res.violations.map((v) => `${v.id}: ${v.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}
