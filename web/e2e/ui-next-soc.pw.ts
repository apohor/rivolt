import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// ui_theme_next Battery (SoC) trend (the iPhone's #895 rules): strip under 0%, today's limit, min/max, crosshair.
// Flag off keeps today's line chart.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    for (const state of ["off", "on"] as const) {
      test(`soc trend ${state} · ${v.name} · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript(([s, t]) => {
          localStorage.setItem("rivolt.ui.next", s);
          localStorage.setItem("rivolt.theme", t);
          // All time, not "this month": the chart spans the period, so near a month's
          // start (or end) the stub's last hours are a sliver with no band to find.
          localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
        }, [state, theme]);
        await stubApi(page, { scenario: "charging" });
        await page.goto("/");
        await page.waitForTimeout(1500);
        await page.getByText("Battery (SoC) trend").first().scrollIntoViewIfNeeded();
        await page.waitForTimeout(700);
        if (state === "off") {
          await expect(page.locator("[data-soc-trend]")).toHaveCount(0);
        } else {
          const chart = page.locator("[data-soc-trend]");
          await expect(chart).toBeVisible();
          await expect(page.locator("[data-band='charge']").first()).toBeAttached();
          await expect(page.getByTestId("soc-limit")).toHaveCount(1);
          // hover() scrolls the plot into view first: a raw mouse.move at a page
          // coordinate can land below the fold on a short or slower-loading page
          // (it did on the Linux CI runner, desktop only).
          const svg = chart.locator("svg[role='img']");
          const box = await svg.boundingBox();
          await svg.hover({ position: { x: box!.width * 0.8, y: Math.min(80, box!.height / 2) } });
          await expect(page.getByTestId("soc-tip")).toContainText(/%/);
          await expect(page.getByTestId("soc-tip")).toContainText(/Charging|Driving|Parked/);
          // Every legend entry corresponds to something drawn, and nothing is promised that is not
          // (no "Pack capacity" row: that line only exists in the legacy chart).
          const entries = await chart.locator("[data-legend]").evaluateAll((els) => els.map((e) => e.getAttribute("data-legend")));
          expect(entries).toEqual(expect.arrayContaining(["line", "limit"]));
          for (const k of entries) {
            expect(await chart.locator(`[data-draw="${k}"]`).count(), `legend ${k} has a drawn element`).toBeGreaterThan(0);
          }
          for (const k of ["line", "charge", "drive", "limit", "extreme"]) {
            if ((await chart.locator(`[data-draw="${k}"]`).count()) > 0) expect(entries, `drawn ${k} has a legend entry`).toContain(k);
          }
          await expect(page.getByText(/Pack capacity/)).toHaveCount(0);
          await expect(chart.locator("[data-legend='limit']")).toContainText("Charge limit now");
          // The limit is today's: a two-day segment labelled "Limit N%", not a line across the period.
          await expect(page.getByTestId("soc-limit")).toContainText(/^Limit \d+%$/);
          const limitW = await page.getByTestId("soc-limit").locator("line").evaluate((l) => Number(l.getAttribute("x2")) - Number(l.getAttribute("x1")));
          expect(limitW).toBeLessThan(box!.width / 2);
          // One strip per stretch below the 0% line, no wash over the plot.
          const bands = chart.locator("[data-band]");
          expect(await chart.locator("[data-band] rect").count()).toBe(await bands.count());
          const stripTop = await chart.locator("[data-band] rect").first().evaluate((r) => Number(r.getAttribute("y")));
          const zero = await chart.locator("svg[role='img'] line").first().evaluate((l) => Number(l.getAttribute("y1")));
          expect(stripTop).toBeGreaterThan(zero);
          // The limit swatch is the same amber as the drawn line.
          const drawn = await page.getByTestId("soc-limit").locator("line").evaluate((l) => getComputedStyle(l).stroke);
          const swatch = await page.getByTestId("legend-limit-swatch").evaluate((l) => getComputedStyle(l).stroke);
          expect(swatch).toBe(drawn);
          expect(swatch).toBe("rgb(245, 158, 11)");
          // Bands are visible marks: the strip on the axis is at least 3px wide.
          const strip = await chart.locator("[data-band] rect").last().evaluate((r) => Number(r.getAttribute("width")));
          expect(strip).toBeGreaterThanOrEqual(3);
        }
        await chart(page).screenshot({ path: path.join(SHOTS, `soc-${v.name}-${theme}-${state}.png`) }).catch(() => {});
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

function chart(page: import("@playwright/test").Page) {
  return page.getByText("Battery (SoC) trend").first().locator("xpath=ancestor::*[contains(@class,'rounded')][1]");
}
