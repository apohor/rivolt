import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// The public demo opens a multi-day sample trip: a stored multi-day plan, shown as saved, with no
// planner call, no re-plan and no hotel suggestions (the demo has neither).
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const day = (index: number, depart: string, from: string, to: string, over?: string) => ({
  index,
  plan: DEMO_PLAN,
  depart_at: depart,
  arrive_at: depart,
  start: { latitude: 34, longitude: -118, label: from },
  end: { latitude: 36, longitude: -115, label: to },
  departure_soc: 90,
  arrival_soc: 24,
  destination_reached: true,
  soc_below_limit: false,
  ...(over ? { overnight: { name: over, parked_hours: 11, l2_kw: 7, added_kwh: 52, post_charge_soc_pct: 80, capped: true } } : {}),
  costs: DEMO_PLAN.costs,
});
const PRESET = {
  id: "la-vegas-2day",
  name: "LA to Las Vegas and back",
  inputs: {
    origin: pt("Los Angeles", 34.05, -118.24),
    destination: pt("Los Angeles", 34.05, -118.24),
    extra_stops: [pt("Sample Inn, Las Vegas", 36.17, -115.14)],
    overnight_flags: [true],
    starting_soc: "90",
    target_soc: "20",
  },
  summary: { distance_mi: 540, duration_min: 490, charge_stops: 2, arrival_soc_pct: 24 },
  saved_trip_id: "st-demo-2day",
};
// The demo serves the planner's multi-day answer as is: days and total, with no kind.
const PLAN = {
  tz: "America/Los_Angeles",
  days: [day(1, "2026-11-14T16:00:00Z", "Los Angeles", "Sample Inn, Las Vegas", "Sample Inn, Las Vegas"), day(2, "2026-11-15T16:00:00Z", "Sample Inn, Las Vegas", "Los Angeles")],
  total: { distance_meters: 870_000, drive_duration_sec: 26_400, charging_duration_sec: 3_000 },
};

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`demo multi-day sample trip: two days and the hotel night, no planner call · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page, { demo: true, signedIn: false });
    const calls: string[] = [];
    await page.route(/\/api\/(trips\/plan|trips\/overnight-candidates|trips\/saved\/[^/]+\/active)/, (r) => {
      calls.push(new URL(r.request().url()).pathname);
      return r.abort();
    });
    await page.route("**/api/demo/trips", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ trips: [...DEMO_PRESETS, PRESET] }) }));
    await page.route(/\/api\/demo\/trips\/la-vegas-2day\/plan$/, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(PLAN) }));
    await page.goto("/demo/trips/plan");
    // The multi-day sample is first in the list, tagged, and described as a round trip.
    const buttons = page.getByTestId("demo-presets").getByRole("button");
    await expect(buttons.first()).toContainText("LA to Las Vegas and back");
    await expect(buttons.first()).toContainText("2 days · hotel night");
    await expect(buttons.first()).toContainText("Los Angeles ↔ Sample Inn, Las Vegas · 2 days · 540 mi");
    await buttons.first().click();
    await expect(page.locator("[data-day-card]")).toHaveCount(2);
    // The result is in sight without scrolling hunting: on a wide screen it sits beside the list, on a phone it is brought up.
    const heading = page.getByText(/^2-day round trip from/);
    await expect(heading).toBeVisible();
    await expect.poll(async () => {
      const b = (await heading.boundingBox())!;
      return b.y >= 0 && b.y + b.height <= v.vp.height;
    }, { message: "the result's heading is within the first screen" }).toBe(true);
    if (v.n === "desktop") {
      const list = (await page.getByTestId("demo-presets").boundingBox())!;
      const res = (await heading.boundingBox())!;
      expect(res.x).toBeGreaterThan(list.x + list.width);
    }
    await expect(page.locator("[data-overnight-row]")).toHaveCount(1);
    await expect(page.locator("[data-overnight-row]")).toContainText("Overnight at Sample Inn, Las Vegas");
    // The demo has no Rivian planner, no hotel suggestions and no truck to re-plan from.
    await expect(page.locator("[data-replan], [data-overnight-suggestions]")).toHaveCount(0);
    expect(calls).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.waitForTimeout(800);
    await page.screenshot({ path: path.join(SHOTS, `demo-multiday-${v.n}-dark.png`), fullPage: true });
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".maplibregl-map").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}
