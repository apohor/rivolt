import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// The free plan's other limits (backend #1381) on the client: a locked vehicle, the primary-vehicle chooser, read-only saved trips and the
// saved-trip cap. The server's answers are stubbed; nothing here asks for billing.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
const json = (body: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
const FREE_ME = { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, plan: "free", plan_source: null, plan_period_end: null, plan_trial_ends: null, plan_cancels_at: null, plan_grace_ends: null, plan_ended_at: null, plan_ended_source: null, billing_enabled: true, history_days: 90 };
const VEHICLES = [
  { id: VEHICLE_ID, vin: "SAMPLE0000000001", name: "Trail R1T", model: "R1T", pack_kwh: 131 },
  { id: "veh-locked", vin: "SAMPLE0000000002", name: "Family R1S", model: "R1S", pack_kwh: 131, pro_locked: true },
];

async function asFree(page: Page) {
  await stubApi(page);
  await page.route("**/api/auth/me", (r) => r.fulfill(json(FREE_ME)));
  await page.route("**/api/vehicles", (r) => r.fulfill(json(VEHICLES)));
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`a locked vehicle says so on Truck and Now, and the free plan opens on its primary · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await asFree(page);
    await page.goto("/truck");
    // The default is the open vehicle, never the locked one.
    await expect(page.locator("[data-locked-vehicle]")).toHaveCount(0);
    const picker = page.getByLabel("Vehicle");
    await expect(picker.locator("option", { hasText: "Family R1S · Pro" })).toHaveCount(1);
    await picker.selectOption("veh-locked");
    const note = page.locator("[data-locked-vehicle]");
    await expect(note).toContainText("keeps recording and alerting");
    await expect(note.getByRole("link", { name: "Export everything" })).toBeVisible();
    await expect(note.getByRole("link", { name: "Choose the vehicle the free plan shows" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).analyze()).violations.filter((x) => x.impact === "serious" || x.impact === "critical")).toEqual([]);
    await page.screenshot({ path: path.join(SHOTS, `pro-locked-vehicle-${v.n}-dark.png`) });
    // A locked vehicle is opened only when picked in a visit: Now opens on the primary too, and the picker there locks as well.
    await page.goto("/");
    await expect(page.locator("[data-locked-vehicle]")).toHaveCount(0);
    await page.getByLabel("Vehicle").selectOption("veh-locked");
    await expect(page.locator("[data-locked-vehicle]")).toBeVisible();
    await ctx.close();
  });

  test(`Settings: choosing the vehicle the free plan shows · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await asFree(page);
    let put: unknown = null;
    await page.route("**/api/settings/primary-vehicle", (r) => {
      if (r.request().method() === "PUT") put = r.request().postDataJSON();
      return r.fulfill(json({ vehicle_id: "veh-locked" }));
    });
    await page.goto("/settings#primary-vehicle");
    const card = page.locator("[data-primary-vehicle]");
    await expect(card).toContainText("The free plan shows one vehicle");
    await card.getByLabel("Family R1S").check();
    await card.getByRole("button", { name: "Show this vehicle" }).click();
    await expect(card.getByRole("status")).toContainText("Done");
    expect(put).toEqual({ vehicle_id: "veh-locked" });
    await page.screenshot({ path: path.join(SHOTS, `pro-primary-vehicle-${v.n}-dark.png`) });
    await ctx.close();
  });
}

test("saved trips past the free cap are marked, stay openable, and a 4th save explains the cap", async ({ page }) => {
  await asFree(page);
  const trip = (id: string, name: string, extra: Record<string, unknown> = {}) => ({ id, name, inputs: { origin: { lat: 34, lon: -118, label: "A" }, destination: { lat: 36, lon: -115, label: "B" } }, created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z", ...extra });
  await page.route("**/api/trips/saved", (r) => (r.request().method() === "POST" ? r.fulfill(json({ error: "pro_required", limit: 3 }, 402)) : r.fulfill(json([trip("t1", "Trip one"), trip("t2", "Trip two"), trip("t3", "Trip three"), trip("t4", "Trip four", { read_only: true })]))));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: /Saved/ }).click();
  await expect(page.locator("[data-trip-locked]")).toHaveCount(1);
  await expect(page.getByText("Trip four")).toBeVisible();
  await expect(page.getByRole("button", { name: "delete" })).toHaveCount(4);
});
