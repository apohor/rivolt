import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// plan_v2: a trip's two ends carry the map's colours everywhere: green start, rose destination, on the map pins, the itinerary's dots and the planner form's
// From and To dots (one definition, plan/endpointColors.ts). Key off: the itinerary and the form keep their own colours, as before.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
const GREEN = "rgb(16, 185, 129)"; // #10b981
const ROSE = "rgb(244, 63, 94)"; // #f43f5e

async function openSaved(page: Page, key: boolean, plan: unknown = DEMO_PLAN) {
  await stubApi(page, key ? { designPreview: ["plan_v2"] } : {});
  const t = DEMO_PRESETS[0];
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-v2", name: t.name, inputs: t.inputs, plan, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /LA to Las Vegas/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await expect(page.locator("[data-plan-area='map'] .maplibregl-marker").first()).toBeVisible();
}
// The dot a row draws on the rail (its ::before), and the dot a map pin draws.
const railDot = (page: Page, kind: "start" | "end") =>
  page.locator(`[data-plan-timeline] > li[data-kind=${kind}]`).first().evaluate((li) => getComputedStyle(li, "::before").backgroundColor);
const mapDots = (page: Page) =>
  page.locator(".maplibregl-marker .trip-route-dot").evaluateAll((els) => els.map((e) => getComputedStyle(e as HTMLElement).backgroundColor));
const formDot = (page: Page, heading: "From" | "To") =>
  page.locator(`[data-plan-field][data-heading=${heading}] [data-plan-dot]`).first().evaluate((e) => getComputedStyle(e).backgroundColor);

for (const theme of ["dark", "light"] as const) {
  for (const [name, w, h] of [["393", 393, 852], ["1440", 1440, 900]] as const) {
    test(`key on: start green and destination rose on the map and in the itinerary · ${name} ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await openSaved(page, true);
      expect(await railDot(page, "start")).toBe(GREEN);
      expect(await railDot(page, "end")).toBe(ROSE);
      const pins = await mapDots(page);
      expect(pins[0]).toBe(GREEN); // the map's own start pin: the itinerary matches it
      expect(pins).toContain(ROSE);
      await page.waitForTimeout(600);
      await page.screenshot({ path: path.join(SHOTS, `colours-single-${name}-${theme}.png`) });
      await ctx.close();
    });
  }
}

test("key off: the itinerary keeps its own colours", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, false);
  expect(await railDot(page, "start")).not.toBe(GREEN);
  expect(await railDot(page, "end")).not.toBe(ROSE);
});

test("key on and off: the planner form's From and To dots", async ({ browser }) => {
  for (const key of [true, false]) {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await ctx.newPage();
    await stubApi(page, key ? { designPreview: ["plan_v2"] } : {});
    await page.goto("/trips/plan");
    await expect(page.locator("[data-plan-field][data-heading=From] [data-plan-dot]")).toBeVisible();
    if (key) {
      expect(await formDot(page, "From")).toBe(GREEN);
      expect(await formDot(page, "To")).toBe(ROSE);
    } else {
      expect(await formDot(page, "From")).not.toBe(GREEN);
      expect(await formDot(page, "To")).not.toBe(ROSE);
    }
    await ctx.close();
  }
});

// A round trip is one pin on the map, in the start's colour: the end row's dot matches it, and nothing on the map is rose.
for (const [name, w, h] of [["393", 393, 852], ["1440", 1440, 900]] as const) {
  test(`key on: round trip · ${name}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: h } });
    const page = await ctx.newPage();
    const base = DEMO_PLAN.Routes[0];
    const first = base.Waypoints[0];
    const last = base.Waypoints[base.Waypoints.length - 1];
    const round = { ...DEMO_PLAN, Routes: [{ ...base, Waypoints: [...base.Waypoints.slice(0, -1), { ...last, Latitude: first.Latitude, Longitude: first.Longitude }] }] };
    await openSaved(page, true, round);
    expect(await railDot(page, "start")).toBe(GREEN);
    expect(await railDot(page, "end")).toBe(GREEN);
    expect(await mapDots(page)).not.toContain(ROSE);
    await page.waitForTimeout(600);
    await page.screenshot({ path: path.join(SHOTS, `colours-round-${name}.png`) });
    await ctx.close();
  });
}

for (const [name, w, h] of [["393", 393, 852], ["1440", 1440, 900]] as const) {
  test(`key on: multi-day, each day's start green and end rose · ${name}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: h } });
    const page = await ctx.newPage();
    await stubApi(page, { designPreview: ["plan_v2"] });
    const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
    const day = (index: number, from: string, to: string) => ({
      index, plan: DEMO_PLAN, depart_at: `2026-10-2${index}T13:00:00Z`, arrive_at: `2026-10-2${index}T21:00:00Z`,
      start: { latitude: 41.88, longitude: -87.63, label: from }, end: { latitude: 41.59, longitude: -93.62, label: to },
      departure_soc: 80, arrival_soc: 24, destination_reached: true, soc_below_limit: false, costs: DEMO_PLAN.costs,
    });
    const body = { days: [day(1, "Chicago", "Des Moines"), day(2, "Des Moines", "Denver")], total: { distance_meters: 1_900_000, drive_duration_sec: 26_400, charging_duration_sec: 3_000 }, tz: "UTC" };
    await page.route("**/api/trips/plan-multiday", (r) => r.fulfill(j(body)));
    await page.route("**/api/trips/saved", (r) =>
      r.fulfill(j([{ id: "st-days", name: "Chicago to Denver", created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z",
        inputs: { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [pt("Peoria", 40.69, -89.59), pt("Des Moines", 41.59, -93.62)], overnight_flags: [false, true] } }])),
    );
    await page.goto("/trips/plan");
    await page.getByRole("tab", { name: "Saved" }).click();
    await page.getByRole("button", { name: /Chicago to Denver/ }).click();
    await page.getByRole("tab", { name: "Planner" }).click();
    await page.getByRole("button", { name: /Plan 2-day trip/ }).click();
    await expect(page.getByText(/Day 2/).first()).toBeVisible();
    const dots = await page.locator("[data-plan-timeline] > li[data-kind=start], [data-plan-timeline] > li[data-kind=end]").evaluateAll((lis) => lis.map((li) => [li.getAttribute("data-kind"), getComputedStyle(li, "::before").backgroundColor]));
    expect(dots.length).toBeGreaterThanOrEqual(4);
    for (const [kind, bg] of dots) expect(bg).toBe(kind === "start" ? GREEN : ROSE);
    await page.waitForTimeout(600);
    await page.screenshot({ path: path.join(SHOTS, `colours-multi-${name}.png`) });
    await ctx.close();
  });
}
