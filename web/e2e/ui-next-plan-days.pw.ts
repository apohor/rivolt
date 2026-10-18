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

async function plan(page: Page, body: object) {
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
    const reqs = await plan(page, { days: DAYS, total: TOTAL, simulated: true, tz: "America/Chicago" });
    expect(reqs).toHaveLength(1);
    const r = reqs[0];
    expect(r.tz).toBe("America/Chicago");
    expect(r.days).toHaveLength(3);
    expect(r.days[0].vias).toHaveLength(1);
    expect(r.days[0].vias[0].label).toBe("Peoria");
    expect(r.days[0].overnight).toMatchObject({ name: "Des Moines", until: "07:30", l2_kw: 7, max_soc_pct: 80 });
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
