import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { DEMO_PLAN, stubApi } from "./stub";
import { e2eNow } from "./clock";

// Re-plan from here: a saved multi-day trip that is under way, planned again from the truck's position
// on an explicit tap, never by itself.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const night = (name: string) => ({ l2_kw: 7, until: "08:00", max_soc_pct: 80, name });
const REQUEST = {
  start_at: "2026-10-20T13:00:00Z",
  tz: "America/Chicago",
  origin: pt("Chicago", 41.88, -87.63),
  days: [
    { vias: [pt("Peoria", 40.69, -89.59)], end: pt("Des Moines", 41.59, -93.62), overnight: night("Des Moines") },
    { end: pt("North Platte", 41.12, -100.77), overnight: night("North Platte") },
    { end: pt("Denver", 39.74, -104.99) },
  ],
};
const day = (index: number, depart: string, from: string, to: string, over?: string) => ({
  index,
  plan: DEMO_PLAN,
  depart_at: depart,
  arrive_at: depart,
  start: { latitude: 41, longitude: -87, label: from },
  end: { latitude: 41, longitude: -93, label: to },
  departure_soc: 80,
  arrival_soc: 24,
  destination_reached: true,
  soc_below_limit: false,
  ...(over ? { overnight: { name: over, parked_hours: 11, l2_kw: 7, added_kwh: 52, post_charge_soc_pct: 80, capped: true } } : {}),
  costs: DEMO_PLAN.costs,
});
const DAYS = [
  day(1, "2026-10-20T13:00:00Z", "Chicago", "Des Moines", "Des Moines"),
  day(2, "2026-10-21T12:30:00Z", "Des Moines", "North Platte", "North Platte"),
  day(3, "2026-10-22T12:30:00Z", "North Platte", "Denver"),
];
const TOTAL = { distance_meters: 1_900_000, drive_duration_sec: 90_000, charging_duration_sec: 6_000 };
const POSITION = { lat: 41.0, lon: -90.0, soc: 61, at: new Date(e2eNow()).toISOString() };

type Body = { origin_from_vehicle?: boolean; start_at?: string; starting_soc?: number; vehicle_id?: string; days: { vias?: unknown[] }[] };

async function open(page: Page, active: object, replan?: { status?: number; body: object }) {
  const reqs: Body[] = [];
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([{ id: "st-md", name: "Chicago to Denver", inputs: { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [pt("Peoria", 40.69, -89.59), pt("Des Moines", 41.59, -93.62), pt("North Platte", 41.12, -100.77)], overnight_flags: [false, true, true] }, plan: { kind: "multiday", request: REQUEST, days: DAYS, total: TOTAL }, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]),
    }),
  );
  await page.route("**/api/trips/saved/st-md/active", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ version: 1, saved_trip_id: "st-md", kind: "multiday", day_count: 3, remaining_days: 3, ...active }) }));
  await page.route("**/api/trips/plan-multiday", (r) => {
    reqs.push(r.request().postDataJSON() as Body);
    return r.fulfill({ status: replan?.status ?? 200, contentType: "application/json", body: JSON.stringify(replan?.body ?? { days: [], total: TOTAL }) });
  });
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Chicago to Denver/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  return reqs;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`re-plan from here mid-drive: asks first, drops passed vias, plans from the truck · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark", timezoneId: "America/Chicago" });
    const page = await ctx.newPage();
    await stubApi(page);
    const reqs = await open(
      page,
      { state: "active", current_day: 1, phase: "driving", position: POSITION, vias_passed: [1] },
      { body: { days: [day(1, "2026-10-20T20:00:00Z", "Current location", "Des Moines", "Des Moines"), day(2, "2026-10-21T12:30:00Z", "Des Moines", "North Platte", "North Platte"), day(3, "2026-10-22T12:30:00Z", "North Platte", "Denver")], total: TOTAL, from_position: POSITION } },
    );
    const panel = page.locator("[data-replan]");
    await expect(panel).toContainText("You're on day 1 of 3.");
    // Nothing is planned until the user says so.
    expect(reqs).toHaveLength(0);
    await panel.getByRole("button", { name: "Re-plan from here" }).click();
    await expect(panel).toContainText("from your truck's current location and charge (61%). The original plan is kept for comparison.");
    await expect(panel).toContainText("Stops you've already passed are left out: Peoria.");
    expect(reqs).toHaveLength(0);
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SHOTS, `replan-confirm-${v.n}-dark.png`), fullPage: true });
    await panel.getByRole("button", { name: "Re-plan", exact: true }).click();
    await expect(page.locator("[data-replan-note]")).toContainText("Re-planned from day 1: from your truck at 61%");
    expect(reqs).toHaveLength(1);
    expect(reqs[0].origin_from_vehicle).toBe(true);
    expect(reqs[0].days).toHaveLength(3);
    expect(reqs[0].days[0].vias).toBeUndefined();
    expect(reqs[0]).not.toHaveProperty("vehicle_id");
    expect(reqs[0]).not.toHaveProperty("starting_soc");
    // The old snapshot warning is replaced by the re-plan note.
    await expect(page.getByText(/Showing saved snapshot/)).toHaveCount(0);
    await expect(page.locator("[data-day-card]")).toHaveCount(3);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.waitForTimeout(600);
    await page.screenshot({ path: path.join(SHOTS, `replan-done-${v.n}-dark.png`), fullPage: true });
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}

test("re-plan on the night: plans the next day from its departure with the projected charge, keeping day 1", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark", timezoneId: "America/Chicago" });
  const page = await ctx.newPage();
  await stubApi(page);
  const reqs = await open(
    page,
    { state: "active", current_day: 1, phase: "overnight", next_day: 2, next_depart_at: "2026-10-21T13:00:00Z", projected_soc: 80, position: POSITION },
    { body: { days: [day(1, "2026-10-21T13:00:00Z", "Des Moines", "North Platte", "North Platte"), day(2, "2026-10-22T12:30:00Z", "North Platte", "Denver")], total: TOTAL, from_position: POSITION } },
  );
  const panel = page.locator("[data-replan]");
  await expect(panel).toContainText("You're parked for the night; day 2 of 3 is next.");
  await panel.getByRole("button", { name: "Re-plan from here" }).click();
  await expect(panel).toContainText("the rest of the trip");
  await panel.getByRole("button", { name: "Re-plan", exact: true }).click();
  await expect(page.locator("[data-replan-note]")).toBeVisible();
  expect(reqs[0].days).toHaveLength(2);
  expect(reqs[0].start_at).toBe("2026-10-21T13:00:00Z");
  expect(reqs[0].starting_soc).toBe(80);
  expect(reqs[0].origin_from_vehicle).toBe(true);
  // Day 1 stays as saved; the new days are numbered 2 and 3.
  await expect(page.locator("[data-day-card]")).toHaveCount(3);
  await expect(page.getByText("Day 3", { exact: true }).first()).toBeVisible();
  await ctx.close();
});

test("no recent reading: the button is off with the reason, and a refusal shows the server's words", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await open(page, { state: "active", current_day: 1, phase: "driving", position: null });
  const panel = page.locator("[data-replan]");
  await expect(panel.getByRole("button", { name: "Re-plan from here" })).toBeDisabled();
  await expect(panel).toContainText("Your truck's position isn't available right now.");
  await expect(panel).toContainText("Try again once it's awake.");
  await ctx.close();

  const ctx2 = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page2 = await ctx2.newPage();
  await stubApi(page2);
  await open(page2, { state: "active", current_day: 1, phase: "driving", position: POSITION }, { status: 409, body: { error: "Your truck's position isn't available right now. Try again once it's awake.", class: "no_position" } });
  await page2.locator("[data-replan]").getByRole("button", { name: "Re-plan from here" }).click();
  await page2.locator("[data-replan]").getByRole("button", { name: "Re-plan", exact: true }).click();
  await expect(page2.getByRole("alert")).toContainText("Your truck's position isn't available right now.");
  await ctx2.close();
});

test("a trip that is not under way has no re-plan", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await open(page, { state: "before", current_day: null, position: POSITION });
  await expect(page.locator("[data-day-card]")).toHaveCount(3);
  await expect(page.locator("[data-replan]")).toHaveCount(0);
  await ctx.close();
});
