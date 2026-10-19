import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// Truck tab "Trends for your truck": charts only when the server's rule is met, honest sentences otherwise,
// and "can't be tracked" where the truck has no capacity signal.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const months = ["2025-12", "2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09", "2026-10"];
const eff = months.map((m, i) => ({ month: m, distance: i === 2 ? 20 : 300 + i * 10, energy_kwh: 100, efficiency: i === 2 ? 2.1 : 2.6 + (i % 4) * 0.15, drives: 14 }));
const samples = Array.from({ length: 9 }, (_, i) => ({ at: new Date(2026, 1 + i, 10).toISOString(), kwh_effective: 124 + (i % 3) * 0.8 }));
const base = { units: "imperial", distance_unit: "mi", efficiency_unit: "mi/kWh", timezone: "America/Chicago", months: 12, min_month_distance: 50, min_months: 3 };
const FULL = { ...base, qualifying_months: 10, efficiency: eff, capacity: { samples, median_kwh: 124.8, nameplate_kwh: 131, qualifying_count: 9, span_days: 240, min_samples: 5, min_span_days: 30 } };
const SPARSE = { ...base, qualifying_months: 1, efficiency: eff.slice(-1), capacity: { samples: samples.slice(0, 2), median_kwh: null, nameplate_kwh: 131, qualifying_count: 2, span_days: 3, min_samples: 5, min_span_days: 30 } };
const R2 = { ...base, qualifying_months: 4, efficiency: eff.slice(-4), capacity: null };

async function open(page: Page, body: unknown, status = 200) {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  await page.route(new RegExp(`/api/vehicles/${VEHICLE_ID}/trends`), (r) => r.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) }));
  await page.goto("/truck");
  await expect(page.getByText("Pack capacity").first()).toBeVisible();
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`trends with enough history draw both charts · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, FULL);
    const card = page.locator("[data-trends]");
    await expect(card.getByRole("img", { name: /Efficiency by month/ })).toBeVisible();
    await expect(card.getByRole("img", { name: /Estimated usable capacity/ })).toBeVisible();
    await expect(card).toContainText("About 125 kWh");
    await expect(card).toContainText("usable (estimate)");
    await expect(card).not.toContainText(/%\s*of\s*(nameplate|spec)|degradation|battery health/i);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await card.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(SHOTS, `trends-${v.n}-dark.png`), fullPage: true });
    await ctx.close();
  });
}

test("sparse history says how far along it is, with no charts", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, SPARSE);
  const card = page.locator("[data-trends]");
  await expect(card).toContainText("1 of 3 months with at least 50 mi");
  await expect(card).toContainText("2 so far");
  await expect(card.getByRole("img")).toHaveCount(0);
  await page.screenshot({ path: path.join(SHOTS, "trends-sparse-iphone-dark.png"), fullPage: true });
  await ctx.close();
});

test("a truck with no capacity signal says so and still shows efficiency", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await open(page, R2);
  const card = page.locator("[data-trends]");
  await expect(card).toContainText("Battery capacity can't be tracked on this truck yet.");
  await expect(card.getByRole("img", { name: /Efficiency by month/ })).toBeVisible();
  await ctx.close();
});

test("a server without the endpoint shows no card; an error offers a retry", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const a = await ctx.newPage();
  await open(a, { error: "not found" }, 404);
  await a.waitForTimeout(800);
  await expect(a.locator("[data-trends]")).toHaveCount(0);
  const b = await ctx.newPage();
  await open(b, { error: "boom" }, 500);
  await expect(b.getByRole("button", { name: "Try again" })).toBeVisible();
  await ctx.close();
});
