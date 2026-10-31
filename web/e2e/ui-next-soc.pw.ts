import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Battery (SoC) trend, calm (the iPhone's rules): a soft daily band and one line, one faint limit, the
// extremes named, a caption and no legend.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    {
      const state = "on";
      test(`soc trend ${state} · ${v.name} · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript((t) => {
          localStorage.setItem("rivolt.theme", t);
          // All time, not "this month": the chart spans the period, so near a month's
          // start (or end) the stub's last hours are a sliver with no band to find.
          localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
        }, theme);
        await stubApi(page, { scenario: "charging" });
        await page.goto("/");
        await page.waitForTimeout(1500);
        await page.getByText("Battery (SoC) trend").first().scrollIntoViewIfNeeded();
        await page.waitForTimeout(700);
        const chart = page.locator("[data-soc-trend]");
        await expect(chart).toBeVisible();
        // The band and the line, no strip, bars or legend.
        await expect(chart.locator("[data-draw='band']").first()).toBeAttached();
        await expect(chart.locator("[data-band]")).toHaveCount(0);
        await expect(chart.locator("[data-soc-legend]")).toHaveCount(0);
        await expect(chart.locator("[data-soc-caption]")).toContainText("Band: each day");
        // One faint limit line, named once ("Limit N%").
        await expect(page.getByTestId("soc-limit")).toHaveCount(1);
        await expect(page.getByTestId("soc-limit")).toContainText(/^Limit \d+%$/);
        expect(await page.getByTestId("soc-limit").locator("line").count()).toBe(1);
        const drawn = await page.getByTestId("soc-limit").locator("line[data-limit-line]").evaluate((l) => getComputedStyle(l).stroke);
        expect(drawn).toBe("rgb(245, 158, 11)");
        // From its last change to now: not a line across the whole period.
        const limitW = await page.getByTestId("soc-limit").locator("line[data-limit-line]").evaluate((l) => l.getBoundingClientRect().width);
        expect(limitW).toBeLessThan((await chart.locator("svg[role='img']").boundingBox())!.width * 0.8);
        // hover() scrolls the plot into view first: a raw mouse.move at a page
        // coordinate can land below the fold on a short or slower-loading page
        // (it did on the Linux CI runner, desktop only).
        const svg = chart.locator("svg[role='img']");
        const box = await svg.boundingBox();
        await svg.hover({ position: { x: box!.width * 0.8, y: Math.min(80, box!.height / 2) } });
        await expect(page.getByTestId("soc-tip")).toContainText(/%/);
        await expect(page.getByTestId("soc-tip")).toContainText(/ended \d+%/);
        // The plot is named for a screen reader in one sentence.
        await expect(svg).toHaveAttribute("aria-label", /^Battery level by day\./);
        await expect(page.getByText(/Battery capacity/)).toHaveCount(0);
        await chartArea(page).screenshot({ path: path.join(SHOTS, `soc-${v.name}-${theme}-${state}.png`) }).catch(() => {});
        const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container, .maplibregl-map").analyze();
        const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
        expect(summary, summary.join("\n")).toEqual([]);
        await ctx.close();
      });
    }
  }
}

function chartArea(page: import("@playwright/test").Page) {
  return page.getByText("Battery (SoC) trend").first().locator("xpath=ancestor::*[contains(@class,'rounded')][1]");
}
