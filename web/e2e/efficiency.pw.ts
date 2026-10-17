import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// Activity → Efficiency (new look): mi/kWh by temperature, speed and drive mode as three
// distribution charts under the period's average; "Not enough data" when there is nothing to chart.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const b = (key: string, v: number | null, drives: number, extra: Record<string, unknown> = {}) => ({ key, mi_per_kwh: v, drives, miles: drives * 18, ...extra });
const FULL = {
  enough_data: true,
  min_drives: 5,
  overall: { mi_per_kwh: 2.31, drives: 34, miles: 710.4, kwh: 307.5 },
  by_temp: [
    b("lt0", 1.62, 6, { hi_c: 0 }), b("0_10", 1.95, 8, { lo_c: 0, hi_c: 10 }), b("10_20", 2.28, 9, { lo_c: 10, hi_c: 20 }),
    b("20_30", 2.52, 8, { lo_c: 20, hi_c: 30 }), b("30p", null, 3, { lo_c: 30 }),
  ],
  by_speed: [
    b("lt35", 2.6, 7, { hi_mph: 35 }), b("35_55", 2.45, 10, { lo_mph: 35, hi_mph: 55 }), b("55_65", 2.15, 9, { lo_mph: 55, hi_mph: 65 }),
    b("65_75", 1.9, 6, { lo_mph: 65, hi_mph: 75 }), b("75p", null, 2, { lo_mph: 75 }),
  ],
  by_mode: [b("everyday", 2.38, 20, { label: "All-Purpose" }), b("conserve", 2.62, 9, { label: "Conserve" }), b("sport", null, 3, { label: "Sport" })],
};

async function open(page: Page, body: unknown | null, status = 200) {
  await stubApi(page, { scenario: "parked" });
  await page.route(`**/api/vehicles/${VEHICLE_ID}/efficiency*`, (r) =>
    status === 404 ? r.fulfill({ status: 404, json: { error: "not found" } }) : r.fulfill({ json: body }),
  );
  await page.goto("/activity?view=efficiency");
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`efficiency distribution charts · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
        localStorage.setItem("rivolt.period", JSON.stringify({ kind: "all", offset: 0 }));
      }, [theme]);
      await open(page, FULL);
      await expect(page.locator("[data-eff-state='ready']")).toBeVisible();
      await expect(page.locator("[data-eff-overall]")).toContainText("2.31");
      for (const k of ["temp", "speed", "mode"]) await expect(page.locator(`[data-eff-chart='${k}']`)).toBeVisible();
      // Bars for buckets with data, an empty slot for the one under the drive floor.
      expect(await page.locator("[data-eff-chart='temp'] [data-eff-bar]").count()).toBe(4);
      expect(await page.locator("[data-eff-chart='temp'] [data-eff-empty]").count()).toBe(1);
      await expect(page.locator("[data-eff-chart='speed'] [data-eff-avg]")).toBeVisible();
      await expect(page.locator("[data-eff-chart='temp'] li[aria-label^='<0°']")).toHaveAttribute("aria-label", /1\.62 mi\/kWh, 6 drives/);
      // The bars order the way the numbers do (taller is more efficient).
      const hs = await page.locator("[data-eff-chart='temp'] [data-eff-bar]").evaluateAll((els) => els.map((e) => e.getBoundingClientRect().height));
      expect(hs[0]).toBeLessThan(hs[3]);
      await page.waitForTimeout(400);
      await page.screenshot({ path: path.join(SHOTS, `efficiency-${w}-${theme}.png`), fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("not enough data says so, and an older server says it isn't available", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  let page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await open(page, { enough_data: false, min_drives: 5, overall: { mi_per_kwh: null, drives: 2, miles: 30, kwh: 14 } });
  await expect(page.locator("[data-eff-state='not-enough']")).toContainText("Not enough data yet");
  await expect(page.locator("[data-eff-chart]")).toHaveCount(0);
  await page.close();
  page = await ctx.newPage();
  await open(page, null, 404);
  await expect(page.locator("[data-eff-state='unavailable']")).toBeVisible();
  await ctx.close();
});

test("the view switcher shows Efficiency in the new look only", async ({ browser }) => {
  for (const ui of ["on", "off"] as const) {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    const page = await ctx.newPage();
    await page.addInitScript((s) => localStorage.setItem("rivolt.ui.next", s), ui);
    await stubApi(page, { scenario: "parked" });
    await page.goto("/activity");
    await expect(page.getByRole("button", { name: "Efficiency", exact: true })).toHaveCount(ui === "on" ? 1 : 0);
    await ctx.close();
  }
});
