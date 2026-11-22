import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// plan_v2, items 2 and 3. Phone: no tall empty map box before there is a route. Totals: the total time once (strip, sheet peek and the map's
// card say the same thing in the same form), with driving and charging beneath it in the strip.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function openSaved(page: Page, key: boolean) {
  await stubApi(page, key ? { designPreview: ["plan_v2"] } : {});
  const t = DEMO_PRESETS[0];
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: "st-v2", name: t.name, inputs: t.inputs, plan: DEMO_PLAN, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]) }),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /LA to Las Vegas/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await expect(page.locator("[data-plan-area='map'] .maplibregl-marker").first()).toBeVisible();
}

for (const theme of ["dark", "light"] as const) {
  for (const key of [true, false]) {
    test(`phone empty state · key ${key ? "on" : "off"} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await stubApi(page, key ? { designPreview: ["plan_v2"] } : {});
      await page.goto("/trips/plan");
      await expect(page.locator("[data-plan-form]")).toBeVisible();
      const empty = page.locator("[data-plan-area='map'][data-empty='true']");
      if (key) await expect(empty).toBeHidden();
      else await expect(empty).toBeVisible();
      await page.waitForTimeout(600);
      await page.screenshot({ path: path.join(SHOTS, `item2-393-${theme}-${key ? "on" : "off"}.png`) });
      await ctx.close();
    });
  }
}

test("desktop keeps the empty map beside the form with the key on", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await stubApi(page, { designPreview: ["plan_v2"] });
  await page.goto("/trips/plan");
  await expect(page.locator("[data-plan-area='map'][data-empty='true']")).toBeVisible();
});

test("key on: the total is said on the map card only (not again in the peek), with driving and charging beneath it in the sheet", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await openSaved(page, true);
  await expect(page.locator(".rivolt-trip-card").first()).toContainText(/charging stop/);
  await expect(page.locator("[data-plan-sheet-title] p")).toHaveCount(0);
  const handle = page.locator("[data-plan-sheet-handle]");
  await handle.click();
  await handle.click();
  const split = page.locator("[data-time-split]");
  await expect(split).toContainText("driving");
  await expect(split).toContainText("charging");
  await expect(page.locator("[data-plan-hero]")).not.toContainText(/stop\s*25\s*min/);
  await page.waitForTimeout(500);
  await page.screenshot({ path: path.join(SHOTS, "item3-393-totals.png") });
});

test("key off: today's totals (no split, charging minutes in the Charging cell)", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await openSaved(page, false);
  const handle = page.locator("[data-plan-sheet-handle]");
  await handle.click();
  await handle.click();
  await expect(page.locator("[data-time-split]")).toHaveCount(0);
  await expect(page.locator("[data-plan-hero]")).toContainText("25");
  await expect(page.locator("[data-plan-sheet-title] p")).toContainText("driving");
});

// A multi-day plan: day 2 holds 40 minutes beyond driving and charging (a rest). "Driving" is the plan's own driving time, and the 40 minutes
// are shown under their own name; they are not counted as driving.
test("key on, multi-day: each day shows one total, no split that could mislabel a rest", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await stubApi(page, { designPreview: ["plan_v2"] });
  const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
  const withRest = { ...DEMO_PLAN, Routes: [{ ...DEMO_PLAN.Routes[0], TotalTripDurationSec: DEMO_PLAN.Routes[0].TotalTripDurationSec + 2400 }] };
  const day = (index: number, plan: typeof DEMO_PLAN, from: string, to: string) => ({
    index, plan, depart_at: `2026-10-2${index}T13:00:00Z`, arrive_at: `2026-10-2${index}T21:00:00Z`,
    start: { latitude: 41.88, longitude: -87.63, label: from }, end: { latitude: 41.59, longitude: -93.62, label: to },
    departure_soc: 80, arrival_soc: 24, destination_reached: true, soc_below_limit: false, costs: DEMO_PLAN.costs,
  });
  const body = {
    days: [day(1, DEMO_PLAN, "Chicago", "Des Moines"), day(2, withRest, "Des Moines", "Denver")],
    total: { distance_meters: 1_900_000, drive_duration_sec: 26_400, charging_duration_sec: 3_000 },
    tz: "UTC",
  };
  await page.route("**/api/trips/plan-multiday", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) }));
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({
      status: 200, contentType: "application/json",
      body: JSON.stringify([{ id: "st-days", name: "Chicago to Denver", created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z",
        inputs: { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [pt("Peoria", 40.69, -89.59), pt("Des Moines", 41.59, -93.62)], overnight_flags: [false, true], max_overnight_soc_pct: 80, overnight_departure_local: "07:30", overnight_l2_kw: 7 } }]),
    }),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Chicago to Denver/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: /Plan 2-day trip/ }).click();
  await expect(page.getByText(/Day 2/).first()).toBeVisible();
  // A multi-day result has no split strip: each day shows its total once (the rest included), so a rest is never labelled driving.
  await expect(page.locator("[data-time-split]")).toHaveCount(0);
  await expect(page.getByText("Day 2 · 270 mi · 1 charging stop · 4h 45m")).toBeVisible();
});

test("key on: a rest inside the total is shown as other, and driving is the plan's driving time", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  const d = DEMO_PLAN.Routes[0];
  const withRest = { ...DEMO_PLAN, Routes: [{ ...d, TotalTripDurationSec: d.TotalTripDurationSec + 2400 }] };
  await stubApi(page, { designPreview: ["plan_v2"] });
  const t = DEMO_PRESETS[0];
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: "st-v2", name: t.name, inputs: t.inputs, plan: withRest, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]) }),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /LA to Las Vegas/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  const handle = page.locator("[data-plan-sheet-handle]");
  await handle.click();
  await handle.click();
  const split = page.locator("[data-time-split]");
  await expect(split).toContainText("3h 40m driving");
  await expect(split).toContainText("25 min charging");
  await expect(page.locator("[data-time-other]")).toContainText("40 min other");
});
