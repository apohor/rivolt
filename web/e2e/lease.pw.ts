import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// Lease tracker (new look): the Truck card (progress bar, plan standing, projected return and
// overage, miles-a-day budget; hidden with no lease) and the Settings form.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const TERMS = { start_date: "2024-11-03", end_date: "2027-11-03", start_odometer_mi: 12, allowed_miles_total: 36000, overage_per_mile: 0.25, currency: "USD" };
const STATUS = {
  used_mi: 21850.4, allowed_to_date_mi: 20130, ahead_behind_mi: -1720.4, days_total: 1095, days_elapsed: 700, days_left: 395,
  projected_total_mi: 39720, projected_return_odometer_mi: 39732, projected_overage_mi: 3720, projected_overage_cost: 930,
  miles_per_day_budget: 35.7, pace_mi_per_day: 31.2, pace_basis: "last_90_days", odometer_mi: 21862.4, odometer_at: new Date().toISOString(),
};

async function mock(page: Page, terms: unknown | null, status: unknown | null) {
  await stubApi(page, { scenario: "parked", drain: "ok" });
  await page.route(`**/api/vehicles/${VEHICLE_ID}/lease/status`, (r) => (status ? r.fulfill({ json: status }) : r.fulfill({ status: 404, json: { error: "none" } })));
  await page.route(`**/api/vehicles/${VEHICLE_ID}/lease`, (r) => {
    const m = r.request().method();
    if (m === "GET") return terms ? r.fulfill({ json: terms }) : r.fulfill({ status: 404, json: { error: "none" } });
    if (m === "PUT") return r.fulfill({ json: r.request().postDataJSON() });
    return r.fulfill({ status: 204, body: "" });
  });
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`Truck lease card · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await mock(page, TERMS, STATUS);
      await page.goto("/truck");
      const card = page.locator("[data-lease-state='ready']");
      await expect(card).toBeVisible();
      await expect(page.locator("[data-lease-used]")).toContainText("21,850");
      await expect(page.locator("[data-lease-used]")).toContainText("36,000 mi");
      await expect(page.locator("[data-lease-standing]")).toHaveText("1,720 mi over plan");
      await expect(page.locator("[data-lease-projected-total]")).toContainText("39,720 mi");
      await expect(page.locator("[data-lease-projected-odo]")).toHaveText("odometer ~39,732 mi");
      await expect(page.locator("[data-lease-overage]")).toContainText("$930");
      await expect(card).toContainText("(last 90 days)");
      // The amber "total allowed" tick is explained in the legend.
      await expect(page.locator("[data-lease-limit]")).toBeVisible();
      await expect(page.locator("[data-lease-legend-limit]")).toContainText("Total allowed · 36,000 mi");
      await expect(page.locator("[data-lease-budget]")).toContainText("35.7 mi");
      const bar = page.getByRole("progressbar", { name: "Lease miles used" });
      await expect(bar).toHaveAttribute("aria-valuenow", "21850");
      await expect(bar).toHaveAttribute("aria-valuetext", /21,850 mi used of 36,000 mi; 20,130 mi allowed to date/);
      // The fill sits between 0 and the plan marker's right side: over the pro-rata plan but under the total.
      const g = await page.evaluate(() => {
        const r = (s: string) => (document.querySelector(s) as HTMLElement).getBoundingClientRect();
        return { track: r("[role='progressbar']"), fill: r("[data-lease-fill]"), plan: r("[data-lease-plan]") };
      });
      expect(g.fill.width).toBeGreaterThan(g.plan.left - g.track.left);
      expect(g.fill.width).toBeLessThan(g.track.width);
      await card.scrollIntoViewIfNeeded();
      await page.waitForTimeout(400);
      await page.screenshot({ path: path.join(SHOTS, `lease-truck-${w}-${theme}.png`) });
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("no lease: the Truck card is hidden; flag off: hidden too", async ({ browser }) => {
  for (const [ui, terms, status] of [["on", null, null], ["off", TERMS, STATUS]] as const) {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    const page = await ctx.newPage();
    await page.addInitScript((s) => localStorage.setItem("rivolt.ui.next", s), ui);
    await mock(page, terms, status);
    await page.goto("/truck");
    await expect(page.getByText("Pack capacity")).toBeVisible();
    await expect(page.locator("[data-lease-state]")).toHaveCount(0);
    await ctx.close();
  }
});

test("Settings: lease form validates, saves in miles, and removes", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await mock(page, null, null);
  await page.route("**/api/vehicles/owned", (r) => r.fulfill({ json: { vehicles: [{ id: "o1", rivian_vehicle_id: VEHICLE_ID, display_name: "Sample R1S", model: "R1S" }] } }));
  let put: unknown = null;
  let deleted = false;
  await page.route(`**/api/vehicles/${VEHICLE_ID}/lease`, (r) => {
    const m = r.request().method();
    if (m === "PUT") {
      put = r.request().postDataJSON();
      return r.fulfill({ json: put });
    }
    if (m === "DELETE") {
      deleted = true;
      return r.fulfill({ status: 204, body: "" });
    }
    return r.fulfill({ status: 404, json: { error: "none" } });
  });
  await page.goto("/settings?tab=vehicle");
  const form = page.locator("[data-lease-form]");
  await expect(form).toBeVisible();
  // Placeholders read as examples, not as typed values.
  await expect(form.getByLabel(/^Miles allowed/)).toHaveAttribute("placeholder", /^e\.g\. /);
  await form.getByRole("button", { name: "Add lease" }).click();
  await expect(form.getByRole("alert")).toHaveCount(5);
  await form.getByLabel(/^Lease start/).fill("2024-11-03");
  await form.getByLabel(/^Lease end/).fill("2027-11-03");
  await form.getByLabel(/Miles allowed/).fill("36000");
  await form.getByLabel(/Odometer at lease start/).fill("12");
  await form.getByLabel(/Overage per/).fill("0.25");
  await form.getByRole("button", { name: "Add lease" }).click();
  await expect(form.locator("[data-lease-saved]")).toBeVisible();
  expect(put).toEqual({ start_date: "2024-11-03", end_date: "2027-11-03", start_odometer_mi: 12, allowed_miles_total: 36000, overage_per_mile: 0.25, currency: "USD" });
  expect(deleted).toBe(false);
  await ctx.close();
});

test("Settings: an existing lease loads into the form and can be removed", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await mock(page, TERMS, STATUS);
  await page.route("**/api/vehicles/owned", (r) => r.fulfill({ json: { vehicles: [{ id: "o1", rivian_vehicle_id: VEHICLE_ID, display_name: "Sample R1S", model: "R1S" }] } }));
  let deleted = false;
  await page.route(`**/api/vehicles/${VEHICLE_ID}/lease`, (r) => {
    if (r.request().method() === "DELETE") {
      deleted = true;
      return r.fulfill({ status: 204, body: "" });
    }
    return r.fulfill({ json: TERMS });
  });
  await page.goto("/settings?tab=vehicle");
  const form = page.locator("[data-lease-form]");
  await expect(form.getByLabel(/^Lease start/)).toHaveValue("2024-11-03");
  await expect(form.getByLabel(/Miles allowed/)).toHaveValue("36000");
  await form.getByRole("button", { name: "Remove lease" }).click();
  await expect.poll(() => deleted).toBe(true);
  await ctx.close();
});

test("a stale odometer pauses the figures and says why", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 900 } });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await mock(page, TERMS, {
    ...STATUS, used_mi: null, ahead_behind_mi: null, projected_total_mi: null, projected_return_odometer_mi: null,
    projected_overage_mi: null, projected_overage_cost: null, miles_per_day_budget: null, pace_mi_per_day: null, odometer_at: "2026-10-02T12:00:00Z",
  });
  await page.goto("/truck");
  await expect(page.locator("[data-lease-stale]")).toContainText("No recent odometer reading");
  await expect(page.locator("[data-lease-used]")).toContainText("—");
  await expect(page.locator("[data-lease-standing]")).toHaveCount(0);
  await expect(page.locator("[data-lease-projected-total]")).toContainText("—");
  await expect(page.locator("[data-lease-projected-odo]")).toHaveCount(0);
  // Paused figures never read as "none": no projection means no overage claim.
  await expect(page.locator("[data-lease-overage]")).toHaveText("—");
  await ctx.close();
});

// Screenshots for review: the card on plan, over plan (overage shown) and with a stale odometer, at
// 390 and 1280 in both themes; and the Settings form at 390 with validation errors showing.
const ON_PLAN = {
  ...STATUS, used_mi: 15210.2, ahead_behind_mi: 4919.8, projected_total_mi: 31140, projected_return_odometer_mi: 31152,
  projected_overage_mi: 0, projected_overage_cost: 0, miles_per_day_budget: 52.8, pace_mi_per_day: 21.7,
};
const STALE = {
  ...STATUS, used_mi: null, ahead_behind_mi: null, projected_total_mi: null, projected_return_odometer_mi: null,
  projected_overage_mi: null, projected_overage_cost: null, miles_per_day_budget: null, pace_mi_per_day: null, odometer_at: "2026-10-02T12:00:00Z",
};
for (const [name, status] of [["on-plan", ON_PLAN], ["over", STATUS], ["stale", STALE]] as const) {
  for (const theme of ["dark", "light"] as const) {
    for (const w of [390, 1280] as const) {
      test(`lease card screenshot · ${name} · ${w}px · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript(([t]) => {
          localStorage.setItem("rivolt.ui.next", "on");
          localStorage.setItem("rivolt.theme", t);
        }, [theme]);
        await mock(page, TERMS, status);
        await page.goto("/truck");
        const card = page.locator("section", { has: page.locator("[data-lease-state]") });
        await expect(card).toBeVisible();
        if (name === "on-plan") await expect(page.locator("[data-lease-overage]")).toHaveText("None projected");
        await page.waitForTimeout(400);
        await card.screenshot({ path: path.join(SHOTS, `lease-card-${name}-${w}-${theme}.png`) });
        await ctx.close();
      });
    }
  }
}

for (const theme of ["dark", "light"] as const) {
  test(`lease form validation screenshot · 390px · ${theme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 900 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => {
      localStorage.setItem("rivolt.ui.next", "on");
      localStorage.setItem("rivolt.theme", t);
    }, [theme]);
    await mock(page, null, null);
    await page.route("**/api/vehicles/owned", (r) => r.fulfill({ json: { vehicles: [{ id: "o1", rivian_vehicle_id: VEHICLE_ID, display_name: "Sample R1S", model: "R1S" }] } }));
    await page.goto("/settings?tab=vehicle");
    const form = page.locator("[data-lease-form]");
    await form.getByRole("button", { name: "Add lease" }).click();
    await expect(form.getByRole("alert")).toHaveCount(5);
    await form.scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
    await page.locator("section", { has: form }).screenshot({ path: path.join(SHOTS, `lease-form-errors-390-${theme}.png`) });
    await ctx.close();
  });
}

test("km mode: the heading, the projection and the overage-rate hint follow the unit", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    localStorage.setItem("rivolt.ui.next", "on");
    localStorage.setItem("rivolt.preferences.v1", JSON.stringify({ distanceUnit: "km", temperatureUnit: "c" }));
  });
  await mock(page, TERMS, STATUS);
  await page.route("**/api/vehicles/owned", (r) => r.fulfill({ json: { vehicles: [{ id: "o1", rivian_vehicle_id: VEHICLE_ID, display_name: "Sample R1S", model: "R1S" }] } }));
  await page.goto("/truck");
  await expect(page.locator("[data-lease-budget]").locator("xpath=preceding-sibling::dt")).toContainText("Kilometers a day");
  await expect(page.locator("[data-lease-projected-total]")).toContainText("km");
  await expect(page.locator("[data-lease-projected-odo]")).toContainText("km");
  await page.goto("/settings?tab=vehicle");
  await expect(page.locator("[data-lease-form]")).toContainText("each kilometer over");
  await expect(page.locator("[data-lease-form]")).not.toContainText("each mile");
  await ctx.close();
});
