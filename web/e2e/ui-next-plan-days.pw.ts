import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { DEMO_PLAN, stubApi } from "./stub";

// Multi-day planning against a stubbed #476 response: days with their own
// dates and times, the Simulated badge, a failed-day banner, and vias sent.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

type MultidayBody = {
  tz: string;
  days: { vias?: { label: string }[]; overnight?: Record<string, unknown> }[];
};

const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const SAVED_INPUTS = {
  origin: pt("Chicago", 41.88, -87.63),
  destination: pt("Denver", 39.74, -104.99),
  extra_stops: [pt("Peoria", 40.69, -89.59), pt("Des Moines", 41.59, -93.62), pt("North Platte", 41.12, -100.77)],
  overnight_flags: [false, true, true],
  max_overnight_soc_pct: 80,
  overnight_departure_local: "07:30",
  overnight_l2_kw: 7,
};

const day = (index: number, depart: string, arrive: string, from: string, to: string, over?: string) => ({
  index,
  plan: DEMO_PLAN,
  depart_at: depart,
  arrive_at: arrive,
  start: { latitude: 41.88, longitude: -87.63, label: from },
  end: { latitude: 41.59, longitude: -93.62, label: to },
  departure_soc: index === 1 ? 90 : 80,
  arrival_soc: 24,
  destination_reached: true,
  soc_below_limit: false,
  ...(over ? { overnight: { name: over, parked_hours: 11, l2_kw: 7, added_kwh: 52, post_charge_soc_pct: 80, capped: true } } : {}),
  costs: DEMO_PLAN.costs,
});

const DAYS = [
  day(1, "2026-10-20T13:00:00Z", "2026-10-20T21:00:00Z", "Chicago", "Des Moines", "Des Moines"),
  day(2, "2026-10-21T12:30:00Z", "2026-10-21T21:30:00Z", "Des Moines", "North Platte", "North Platte"),
  day(3, "2026-10-22T12:30:00Z", "2026-10-22T19:00:00Z", "North Platte", "Denver"),
];
const TOTAL = { distance_meters: 1_900_000, drive_duration_sec: 90_000, charging_duration_sec: 6_000 };

async function plan(page: Page, body: object, edit?: () => Promise<void>) {
  const reqs: MultidayBody[] = [];
  await page.route("**/api/trips/plan-multiday", (r) => {
    reqs.push(r.request().postDataJSON() as MultidayBody);
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([{ id: "st-days", name: "Chicago to Denver", inputs: SAVED_INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]),
    }),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Chicago to Denver/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await edit?.();
  await page.getByRole("button", { name: "Plan 3-day trip" }).click();
  await expect(page.getByText(/Day 3/).first()).toBeVisible();
  return reqs;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`3-day plan: dates, own times, Simulated, vias sent · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark", timezoneId: "America/Chicago" });
    const page = await ctx.newPage();
    await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
    await stubApi(page);
    const night2 = () => page.locator("[data-overnight-stop]").nth(1);
    const reqs = await plan(page, { days: DAYS, total: TOTAL, simulated: true, tz: "America/Chicago" }, async () => {
      // One via and two nights: each night has its own fields; change only the second.
      await expect(page.locator("[data-overnight-stop]")).toHaveCount(2);
      await night2().getByLabel("Charger at the stop").selectOption("0");
      await night2().getByLabel("Leave next morning at").fill("06:15");
    });
    expect(reqs).toHaveLength(1);
    const r = reqs[0];
    expect(r.tz).toBe("America/Chicago");
    expect(r.days).toHaveLength(3);
    expect(r.days[0].vias).toHaveLength(1);
    expect(r.days[0].vias[0].label).toBe("Peoria");
    expect(r.days[0].overnight).toMatchObject({ name: "Des Moines", until: "07:30", l2_kw: 7, max_soc_pct: 80 });
    expect(r.days[1].overnight).toMatchObject({ name: "North Platte", until: "06:15", l2_kw: 0 });
    expect(r.days[2].overnight).toBeUndefined();
    await expect(page.getByText("Simulated", { exact: true })).toBeVisible();
    for (const d of ["Day 1", "Day 2", "Day 3"]) await expect(page.getByText(d, { exact: true }).first()).toBeVisible();
    // The night is its own row between the days, with the charge it adds.
    const nights = page.locator("[data-overnight-row]");
    await expect(nights).toHaveCount(2);
    await expect(nights.first()).toContainText("Overnight at Des Moines");
    await expect(nights.first()).toContainText("adds about 52 kWh");
    // Every day is on the page, not just the last one.
    await expect(page.locator("[data-day-card]")).toHaveCount(3);
    // Each day shows its own date.
    await expect(page.getByText(/Oct 21/).first()).toBeVisible();
    await expect(page.getByText(/Oct 22/).first()).toBeVisible();
    await page.waitForTimeout(900);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.screenshot({ path: path.join(SHOTS, `days-${v.n}-dark.png`), fullPage: true });
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });

  test(`failed day: the days so far stay, with a plain banner · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "light" });
    const page = await ctx.newPage();
    await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
    await stubApi(page);
    await plan(page, { days: DAYS.slice(0, 2), total: TOTAL, failed_day: 3, error: "no route found", tz: "UTC" }).catch(() => {});
    await expect(page.getByRole("alert")).toContainText("Planned through day 2");
    await expect(page.getByRole("alert")).toContainText("Day 3");
    await expect(page.getByText("Simulated", { exact: true })).toHaveCount(0);
    await page.waitForTimeout(900);
    await page.screenshot({ path: path.join(SHOTS, `days-failed-${v.n}-light.png`), fullPage: true });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}

test("add an overnight from the route bar: the hint, the row, then a 2-day plan", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  const reqs: MultidayBody[] = [];
  await page.route("**/api/trips/plan-multiday", (r) => {
    reqs.push(r.request().postDataJSON() as MultidayBody);
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ days: DAYS.slice(0, 2), total: TOTAL, tz: "UTC" }) });
  });
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([{ id: "st-one", name: "Chicago to Denver", inputs: { ...SAVED_INPUTS, extra_stops: [], overnight_flags: [] }, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]),
    }),
  );
  await page.route("**/api/geocode*", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ name: "Omaha", admin1: "Nebraska", country: "United States", latitude: 41.26, longitude: -95.93 }]) }),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Chicago to Denver/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  const bar = page.locator("[data-plan-bar]");
  // Visible without opening More options, with the one-line hint.
  await expect(bar.getByText(/Going further than a day\?/)).toBeVisible();
  await bar.getByRole("button", { name: "+ Add an overnight" }).click();
  await bar.getByPlaceholder("Search city or place…").fill("Omaha");
  await page.getByRole("button", { name: /Omaha/ }).click();
  await expect(bar.locator("[data-overnight-stop]")).toHaveCount(1);
  await expect(bar.locator("[data-overnight-stop]")).toContainText("Omaha");
  await expect(bar.getByText(/Going further than a day\?/)).toHaveCount(0);
  await page.screenshot({ path: path.join(SHOTS, "days-add-overnight-iphone-dark.png"), fullPage: true });
  await page.getByRole("button", { name: "Plan 2-day trip" }).click();
  await expect(page.getByText("Day 2", { exact: true }).first()).toBeVisible();
  expect(reqs).toHaveLength(1);
  expect(reqs[0].days).toHaveLength(2);
  expect(reqs[0].days[0].overnight).toMatchObject({ name: expect.stringContaining("Omaha"), l2_kw: 7, until: "07:30" });
  await ctx.close();
});

test("a long day suggests hotels with a listed charger; Use makes one the night with its kW", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark", timezoneId: "America/Chicago" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  const asked: { path?: unknown[]; budget_hours?: number }[] = [];
  await page.route("**/api/trips/overnight-candidates", (r) => {
    asked.push(r.request().postDataJSON());
    return r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        candidates: [
          { name: "Sample Inn, Ogallala", lat: 41.13, lon: -101.72, l2_kw: 19, l2_kw_assumed: false, detour_m: 6000, along_m: 450000 },
          { name: "Sample Suites, Sidney", lat: 41.14, lon: -102.98, l2_kw: 7, l2_kw_assumed: true, detour_m: 3000, along_m: 470000 },
        ],
        target: { lat: 41.13, lon: -101.8, along_m: 452000, reaches_end: false },
        route_m: 900000,
        source: "listing",
      }),
    });
  });
  const longDay = { ...DAYS[2], plan: { ...DEMO_PLAN, Routes: [{ ...DEMO_PLAN.Routes[0], TotalDriveDurationSec: 40_000 }] } };
  await plan(page, { days: [DAYS[0], DAYS[1], longDay], total: TOTAL, tz: "America/Chicago" });
  const card = page.locator("[data-overnight-suggestions]");
  await expect(card).toHaveCount(1);
  await expect(card).toContainText("Where to sleep on day 3");
  await expect(card).toContainText("up to 19 kW");
  await expect(card).toContainText("power not listed (7 kW assumed)");
  await expect(card).toContainText("Listed chargers only; availability isn't checked.");
  expect(asked).toHaveLength(1);
  expect(asked[0].budget_hours).toBe(8);
  await page.screenshot({ path: path.join(SHOTS, "days-sleep-desktop-dark.png"), fullPage: true });
  await card.getByRole("button", { name: "Use Sample Inn, Ogallala as the overnight stop" }).click();
  const nights = page.locator("[data-overnight-stop]");
  await expect(nights).toHaveCount(3);
  await expect(nights.nth(2)).toContainText("Sample Inn, Ogallala");
  // The hotel's listed charger is on that night only.
  await expect(nights.nth(2).getByLabel("Charger at the stop")).toHaveValue("19");
  await expect(nights.nth(0).getByLabel("Charger at the stop")).toHaveValue("7");
  await ctx.close();
});

test("a saved multi-day trip keeps its days and reopens with no planner call", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark", timezoneId: "America/Chicago" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  let saved: { plan?: { kind?: string; request?: { tz?: string; days?: unknown[] }; days?: unknown[]; total?: unknown } } | null = null;
  await page.route("**/api/trips/saved/st-days", (r) => {
    saved = r.request().postDataJSON();
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ id: "st-days", name: "Chicago to Denver", inputs: SAVED_INPUTS, plan: saved?.plan, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-18T00:00:00Z" }) });
  });
  await plan(page, { days: DAYS, total: TOTAL, simulated: true, tz: "America/Chicago" });
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: "Update", exact: true }).click();
  await expect.poll(() => saved?.plan?.kind).toBe("multiday");
  // The planner's answer is stored as it was, with the request that produced it.
  expect(saved!.plan!.days).toHaveLength(3);
  expect(saved!.plan!.total).toMatchObject({ distance_meters: TOTAL.distance_meters });
  expect(saved!.plan!.request?.tz).toBe("America/Chicago");
  expect(saved!.plan!.request?.days).toHaveLength(3);

  // Reopen it fresh: the days come from the snapshot and the planner is not called.
  const page2 = await ctx.newPage();
  await page2.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page2);
  let planCalls = 0;
  await page2.route("**/api/trips/plan-multiday", (r) => {
    planCalls++;
    return r.abort();
  });
  await page2.route("**/api/trips/saved", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: "st-days", name: "Chicago to Denver", inputs: SAVED_INPUTS, plan: saved!.plan, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-18T00:00:00Z" }]) }),
  );
  await page2.goto("/trips/plan");
  await page2.getByRole("tab", { name: "Saved" }).click();
  await page2.getByRole("button", { name: /Chicago to Denver/ }).click();
  await page2.getByRole("tab", { name: "Planner" }).click();
  await expect(page2.locator("[data-day-card]")).toHaveCount(3);
  await expect(page2.locator("[data-overnight-row]")).toHaveCount(2);
  await expect(page2.getByText("Simulated", { exact: true })).toBeVisible();
  expect(planCalls).toBe(0);
  await page2.waitForTimeout(800);
  await page2.screenshot({ path: path.join(SHOTS, "days-reopened-desktop-dark.png"), fullPage: true });
  await ctx.close();
});
