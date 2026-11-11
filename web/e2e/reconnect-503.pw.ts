import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi, VEHICLE_ID, vehicleState } from "./stub";

// Single ownership: when the server that holds the truck's Rivian connection does not answer, the API says 503 with Retry-After: 5.
// The live card says "Reconnecting to your vehicle" instead of the raw error; the planner retries once, then says "Reconnecting to Rivian".
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };
const j = (b: unknown, status = 200, headers: Record<string, string> = {}) => ({ status, contentType: "application/json", headers, body: JSON.stringify(b) });
// /live reads every key of the state, so the stub's truck goes over the full fixture. Charging polls every 5 s.
const FULL_STATE = {
  ...JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", "state_full.json"), "utf8")),
  ...vehicleState("charging"),
};
const OWNER_503 = (body: Record<string, string>, ra = "5") => j(body, 503, { "Retry-After": ra });
const PLAN_503 = { error: "the vehicle's owner did not answer", class: "outage", reason: "the vehicle's owner did not answer" };

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
]) {
  test(`@webkit live card: a 503 with Retry-After keeps the last values and says reconnecting · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp });
    const page = await ctx.newPage();
    await stubApi(page);
    let down = false;
    let polls = 0;
    await page.route(`**/api/state/${VEHICLE_ID}`, (r) => {
      if (!down) return r.fulfill(j(FULL_STATE));
      polls++;
      return r.fulfill(OWNER_503({ error: "the vehicle's owner did not answer; try again", class: "unavailable" }));
    });
    await page.goto("/live");
    await expect(page.getByText("Battery").first()).toBeVisible();
    down = true;
    await expect(page.getByRole("status").filter({ hasText: "Reconnecting to your vehicle. These are the last known values." })).toBeVisible({ timeout: 15_000 });
    expect(polls).toBeGreaterThan(0);
    await expect(page.getByText("Battery").first()).toBeVisible();
    await expect(page.getByText(/owner did not answer|Service unavailable/)).toHaveCount(0);
    await ctx.close();
  });

  test(`@webkit live card: with nothing to show yet, a 503 with Retry-After says reconnecting, not the raw error · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route(`**/api/state/${VEHICLE_ID}`, (r) => r.fulfill(OWNER_503({ error: "the vehicle's owner did not answer; try again", class: "unavailable" })));
    await page.goto("/live");
    await expect(page.getByRole("status").filter({ hasText: "Reconnecting to your vehicle. This updates on its own in a moment." })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText(/owner did not answer|Service unavailable/)).toHaveCount(0);
    await ctx.close();
  });

  test(`@webkit planner: two 503s with Retry-After say reconnecting to Rivian after exactly one retry · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp });
    const page = await ctx.newPage();
    await plannerStubs(page);
    let plans = 0;
    await page.route("**/api/trips/plan", (r) => {
      plans++;
      return r.fulfill(OWNER_503(PLAN_503, "1"));
    });
    await planTrip(page);
    await expect(page.getByText("Reconnecting to Rivian.")).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("Nothing is wrong with your trip. Try again in a moment.")).toBeVisible();
    await expect(page.getByText(/Rivian's planner|owner did not answer|your vehicle/)).toHaveCount(0);
    expect(plans).toBe(2);
    await ctx.close();
  });

  test(`@webkit planner: a 503 with Retry-After then a plan shows the plan with no error · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp });
    const page = await ctx.newPage();
    await plannerStubs(page);
    let plans = 0;
    await page.route("**/api/trips/plan", (r) => (plans++ === 0 ? r.fulfill(OWNER_503(PLAN_503, "1")) : r.fulfill(j(DEMO_PLAN))));
    await planTrip(page);
    await expect(page.locator("[data-plan-timeline]").first()).toBeVisible({ timeout: 15_000 });
    expect(plans).toBe(2);
    await expect(page.getByText(/Reconnecting|Rivian's planner|owner did not answer/)).toHaveCount(0);
    await ctx.close();
  });
}

async function plannerStubs(page: Page) {
  await stubApi(page);
  await page.route("**/api/config", (r) =>
    r.fulfill(j({ features: { trip_planner_enabled: true, signup_enabled: true }, tiles: { url: "/api/maps/tiles/us.pmtiles", chargers_url: "/api/maps/tiles/chargers.pmtiles" }, valhalla: { path: "/api/maps/valhalla" }, impersonation: { disabled: true } })),
  );
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
}

async function planTrip(page: Page) {
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
}
