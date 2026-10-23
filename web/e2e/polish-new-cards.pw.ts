import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// Polish pass over the newer cards: axe (WCAG 2.1 AA) in dark and light at 390 and 1280, and the share sheet by keyboard.
const j = (b: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(b) });
const months = ["2025-12", "2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09", "2026-10"];
const eff = months.map((m, i) => ({ month: m, distance: 300 + i * 10, energy_kwh: 100, efficiency: 2.6 + (i % 4) * 0.15, drives: 14 }));
const samples = Array.from({ length: 9 }, (_, i) => ({ at: new Date(2026, 1 + i, 10).toISOString(), kwh_effective: 124 + (i % 3) * 0.8 }));
const TRENDS = {
  units: "imperial", distance_unit: "mi", efficiency_unit: "mi/kWh", timezone: "America/Chicago", months: 12, min_month_distance: 50, min_months: 3, qualifying_months: 10, efficiency: eff,
  capacity: { samples, median_kwh: 124.8, spec_kwh: 131, reported_kwh: 129, qualifying_count: 9, span_days: 240, min_samples: 5, min_span_days: 30 },
};
const COLD = { units: "imperial", distance_unit: "mi", efficiency_unit: "mi/kWh", temp_c: 4.4, efficiency_low: 2.4, efficiency_high: 2.8, range_low: 190, range_high: 215, based_on: { drives: 14, miles: 312, temp_lo_c: 0.4, temp_hi_c: 8.4 }, available: [4.4, 15.6, 26.7], min_drives: 5, min_miles: 50 };

async function truck(page: Page, theme: "dark" | "light") {
  await page.addInitScript(([t]) => {
    localStorage.setItem("rivolt.theme", t);
  }, [theme]);
  await stubApi(page, { drain: "ok" });
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/trends`), (r) => r.fulfill(j(TRENDS)));
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/cold-range`), (r) => r.fulfill(j(COLD)));
  await page.goto("/truck");
  await expect(page.locator("[data-trends]")).toBeVisible();
  await expect(page.locator("[data-cold-range]")).toBeVisible();
  await expect(page.locator("[data-drain-split='ok']")).toBeVisible();
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`Truck tab cards pass axe · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await truck(page, theme);
      const res = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
        .include("[data-trends]")
        .include("[data-cold-range]")
        .include("[data-drain-split]")
        .analyze();
      const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      await ctx.close();
    });
  }
}

test("the share sheet works by keyboard: focus inside, Tab stays in, Escape closes and returns focus", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true });
  await page.goto("/demo/money");
  const trigger = page.getByRole("button", { name: "Share statement" });
  await trigger.focus();
  await page.keyboard.press("Enter");
  const dlg = page.getByRole("dialog");
  await expect(dlg).toBeVisible();
  // Focus starts inside the dialog, and Tab never leaves it.
  expect(await page.evaluate(() => !!document.activeElement?.closest("[role=dialog]"))).toBe(true);
  for (let i = 0; i < 8; i++) {
    await page.keyboard.press("Tab");
    expect(await page.evaluate(() => !!document.activeElement?.closest("[role=dialog]")), `Tab ${i + 1} stays in the dialog`).toBe(true);
  }
  await page.keyboard.press("Escape");
  await expect(dlg).toHaveCount(0);
  await expect(trigger).toBeFocused();
  await ctx.close();
});

for (const theme of ["dark", "light"] as const) {
  test(`the share sheet passes axe · ${theme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
    await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true });
    await page.goto("/demo/money");
    await page.getByRole("button", { name: "Share statement" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).include("[data-share-sheet]").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}
