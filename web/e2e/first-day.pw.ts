import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import path from "node:path";
import { stubApi, VEHICLE_ID } from "./stub";

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");

async function open(page: import("@playwright/test").Page, opts: { rate?: number; lease?: boolean; history?: boolean } = {}) {
  await stubApi(page);
  const { rate = 0, lease = false, history = false } = opts;
  if (!history) {
    await page.route("**/api/drives*", (r) => (r.request().url().includes("/api/drives/") ? r.fallback() : r.fulfill({ json: [] })));
    await page.route("**/api/charges*", (r) => (r.request().url().includes("/api/charges/") ? r.fallback() : r.fulfill({ json: [] })));
  }
  await page.route("**/api/settings/charging/", (r) => r.fulfill({ json: { home_price_per_kwh: rate, home_currency: "USD", gas_price_per_gallon: 4, comparison_mpg: 20 } }));
  await page.route(`**/api/vehicles/${VEHICLE_ID}/lease`, (r) =>
    lease ? r.fulfill({ json: { start_date: "2024-11-03", end_date: "2027-11-03", start_odometer_mi: 12, allowed_miles_total: 36000, overage_per_mile: 0.25, currency: "USD" } }) : r.fulfill({ status: 404, json: { error: "no lease" } }),
  );
  await page.goto("/");
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`first day card · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page);
      const card = page.locator("[data-first-day]");
      await expect(card).toBeVisible();
      await expect(card.getByRole("link", { name: "Set home rate" })).toHaveAttribute("href", "/settings?tab=charging#cost");
      await expect(card.getByRole("link", { name: "Add lease" })).toHaveAttribute("href", "/settings#lease");
      await expect(card.locator("[data-first-day-step=todo]")).toHaveCount(2);
      await expect(card).toContainText("Your first day with Rivolt");
      await expect(card).toContainText("stay unpriced; setting a rate doesn't change them");
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `first-day-${w}-${theme}.png`) });
      await ctx.close();
    });
  }
}

test("steps already done show as done, and Dismiss sticks", async ({ page }) => {
  await open(page, { rate: 0.12, lease: true });
  const card = page.locator("[data-first-day]");
  await expect(card.locator("[data-first-day-step=done]")).toHaveCount(2);
  await expect(card).toContainText("Home rate set: $0.12/kWh");
  await expect(card).toContainText("Lease added");
  await card.getByRole("button", { name: "Hide this" }).click();
  await expect(card).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole("heading", { name: "Now" }).or(page.locator("[data-ui-grid=now]")).first()).toBeVisible();
  await expect(card).toHaveCount(0);
});

test("not shown once there is history", async ({ page }) => {
  await open(page, { history: true });
  await expect(page.locator("[data-ui-grid=now]")).toBeVisible();
  await page.waitForTimeout(500);
  await expect(page.locator("[data-first-day]")).toHaveCount(0);
});
