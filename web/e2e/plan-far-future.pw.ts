import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { DEMO_PLAN, stubApi } from "./stub";

// A plan for a trip more than a month out (far_future:true from the server) says
// the stops, chargers and arrival times are from today's network and conditions,
// and to plan again within a month of leaving. Single-day, multi-day, and a
// reopened saved trip; a near trip has no note.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const NOTE = /today's network and conditions, not the date you leave/;

const day = (index: number, depart: string, arrive: string, from: string, to: string) => ({
  index,
  plan: DEMO_PLAN,
  depart_at: depart,
  arrive_at: arrive,
  start: { latitude: 41.88, longitude: -87.63, label: from },
  end: { latitude: 41.59, longitude: -93.62, label: to },
  departure_soc: 90,
  arrival_soc: 24,
  destination_reached: true,
  soc_below_limit: false,
  costs: DEMO_PLAN.costs,
});
const DAYS = [
  day(1, "2027-01-20T13:00:00Z", "2027-01-20T21:00:00Z", "Chicago", "Des Moines"),
  day(2, "2027-01-21T12:30:00Z", "2027-01-21T21:30:00Z", "Des Moines", "Denver"),
];
const TOTAL = { distance_meters: 1_900_000, drive_duration_sec: 90_000, charging_duration_sec: 6_000 };

const saved = (id: string, name: string, plan: unknown, extra: object = {}) => ({
  id,
  name,
  inputs: { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], ...extra },
  ...(plan ? { plan } : {}),
  created_at: "2026-10-17T00:00:00Z",
  updated_at: "2026-10-17T00:00:00Z",
});

async function setup(page: Page, trips: object[]) {
  await stubApi(page);
  await page.route("**/api/trips/saved", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(trips) }));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
}

async function openSaved(page: Page, name: RegExp) {
  await page.getByRole("button", { name }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
}

test("single-day plan for a far trip shows the note; a near trip does not", async ({ page }) => {
  await setup(page, [saved("st-far", "Far trip", undefined)]);
  // After stubApi: a later route wins.
  await page.route("**/api/trips/plan", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ...DEMO_PLAN, far_future: true }) }));
  await openSaved(page, /Far trip/);
  await page.getByRole("button", { name: "Plan trip" }).click();
  const note = page.locator("[data-far-future-note]");
  await expect(note).toBeVisible();
  await expect(note).toContainText(NOTE);
  await expect(note).toContainText(/within a month of leaving/);
  const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).exclude(".maplibregl-map").analyze();
  expect(res.violations.map((v) => v.id)).toEqual([]);

  // The same trip planned for a near date: no note.
  await page.unroute("**/api/trips/plan");
  await page.route("**/api/trips/plan", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(DEMO_PLAN) }));
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.locator("[data-far-future-note]")).toHaveCount(0);
});

test("multi-day plan for a far trip shows the note", async ({ page }) => {
  await setup(page, [
    saved("st-md", "Far two-day", undefined, {
      extra_stops: [pt("Des Moines", 41.59, -93.62)],
      overnight_flags: [true],
      max_overnight_soc_pct: 80,
      overnight_departure_local: "07:30",
      overnight_l2_kw: 7,
    }),
  ]);
  await page.route("**/api/trips/plan-multiday", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ days: DAYS, total: TOTAL, tz: "UTC", far_future: true }) }),
  );
  await openSaved(page, /Far two-day/);
  await page.getByRole("button", { name: /Plan 2-day trip/ }).click();
  await expect(page.locator("[data-day-card]")).toHaveCount(2);
  await expect(page.locator("[data-far-future-note]")).toContainText(NOTE);
});

test("a saved far trip reopens with the note, and a near one without", async ({ page }) => {
  await setup(page, [
    saved("st-saved-far", "Saved far", { kind: "multiday", days: DAYS, total: TOTAL, far_future: true }),
    saved("st-saved-near", "Saved near", { kind: "multiday", days: DAYS, total: TOTAL }),
  ]);
  await openSaved(page, /Saved far/);
  await expect(page.locator("[data-day-card]").first()).toBeVisible();
  await expect(page.locator("[data-far-future-note]")).toContainText(NOTE);
  await page.getByRole("tab", { name: "Saved" }).click();
  await openSaved(page, /Saved near/);
  await expect(page.locator("[data-day-card]").first()).toBeVisible();
  await expect(page.locator("[data-far-future-note]")).toHaveCount(0);
});
