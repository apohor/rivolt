import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// The elevation chart under the trip summary, linked to the map: pointing at or dragging along the chart moves a dot on the route, and a tap on
// the route puts the chart's marker there. Checked on a 390 px iPhone, at Safari's aA 300% (130 px wide) and on a desktop. Units follow the account
// (ft and mi here); the numbers are a generated hill, not a real road.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Las Vegas", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
const TOTAL = 434_500;
const PLAN = {
  ...DEMO_PLAN,
  elevation_profile: {
    points: Array.from({ length: 41 }, (_, i) => [Math.round((TOTAL * i) / 40), Math.round(300 + 600 * Math.sin((i / 40) * Math.PI * 2) ** 2)]),
    waypoint_at_m: [0, 215_000, TOTAL],
  },
};

async function openPlan(page: Page, plan: unknown) {
  await stubApi(page);
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(plan)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.locator("[data-plan-sheet], [data-plan-area=itinerary]").first()).toBeVisible();
}

async function chartAt(page: Page, share: number) {
  await page.locator("[data-elevation-chart]").scrollIntoViewIfNeeded();
  const box = (await page.locator("[data-elevation-chart]").boundingBox())!;
  // The plot spans x 10..320 of a 330-wide viewBox.
  return { x: box.x + ((10 + share * 310) / 330) * box.width, y: box.y + box.height * 0.5 };
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 }, sheet: true },
  { n: "desktop", vp: { width: 1280, height: 900 }, sheet: false },
] as const) {
  test(`@webkit elevation chart is linked to the map · ${v.n}`, async ({ browser }, info) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await openPlan(page, PLAN);
    if (v.sheet) await page.locator("[data-plan-sheet-handle]").click(); // half open: the chart and the map both show
    const chart = page.locator("[data-elevation-chart]");
    await expect(chart).toBeVisible();
    await expect(page.locator("[data-elevation-readout]")).toHaveText(/^[\d,]+ to [\d,]+ ft$/);
    await expect(page.locator("[data-elevation-stop]")).toHaveCount(1);
    await expect(page.getByText("Rivian's estimate decides the energy")).toBeVisible();

    // Dragging along the chart moves the dot on the map and the readout.
    const dot = page.locator("[data-elevation-dot]");
    await expect(dot).toHaveCount(0);
    const a = await chartAt(page, 0.2);
    await page.mouse.move(a.x, a.y);
    await expect(page.locator("[data-elevation-readout]")).toHaveText(/^5[3-5] mi · [\d,]+ ft$/);
    await expect(dot).toBeVisible();
    const at20 = (await dot.boundingBox())!;
    const b = await chartAt(page, 0.8);
    await page.mouse.down();
    await page.mouse.move(b.x, b.y, { steps: 6 });
    await page.mouse.up();
    await expect(page.locator("[data-elevation-readout]")).toHaveText(/^21[5-7] mi · [\d,]+ ft$/);
    const at80 = (await dot.boundingBox())!;
    expect(Math.abs(at80.x - at20.x) + Math.abs(at80.y - at20.y), "the dot moved along the route").toBeGreaterThan(20);
    const mapBox = (await page.locator("[data-plan-area=map]").boundingBox())!;
    expect(at80.x).toBeGreaterThanOrEqual(mapBox.x);
    expect(at80.x + at80.width).toBeLessThanOrEqual(mapBox.x + mapBox.width + 1);

    // Keyboard: arrows move it too.
    await chart.focus();
    await page.keyboard.press("Home");
    await expect(page.locator("[data-elevation-readout]")).toHaveText(/^0 mi · /);
    await page.keyboard.press("End");
    await expect(page.locator("[data-elevation-readout]")).toHaveText(/^270 mi · /);

    // A tap on the route puts the chart's marker there: park the chart at 20%, tap where the dot was at 80%.
    await page.keyboard.press("Home");
    await expect(page.locator("[data-elevation-readout]")).toHaveText(/^0 mi/);
    await page.mouse.click(at80.x + at80.width / 2, at80.y + at80.height / 2);
    await expect(page.locator("[data-elevation-readout]")).toHaveText(/^21\d mi · /);
    await expect(page.locator("[data-elevation-cursor]")).toBeVisible();

    const shot = info.outputPath(`elevation-${v.n}.png`);
    await page.screenshot({ path: shot });
    await info.attach(`elevation-${v.n}`, { path: shot, contentType: "image/png" });
  });
}

test("@webkit elevation chart at aA 300% fits and drags", async ({ browser }, info) => {
  const ctx = await browser.newContext({ viewport: { width: 130, height: 281 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await openPlan(page, PLAN);
  const handle = page.locator("[data-plan-sheet-handle]");
  await handle.click();
  await handle.click();
  await expect(page.locator("[data-plan-sheet]")).toHaveAttribute("data-snap", "full");
  const chart = page.locator("[data-elevation-chart]");
  await chart.scrollIntoViewIfNeeded();
  await expect(chart).toBeVisible();
  const box = (await chart.boundingBox())!;
  expect(box.width).toBeGreaterThan(80);
  expect(box.x + box.width).toBeLessThanOrEqual(130);
  const p = await chartAt(page, 0.5);
  await page.mouse.move(p.x, p.y);
  await page.mouse.down();
  await page.mouse.up();
  await expect(page.locator("[data-elevation-readout]")).toHaveText(/^13[4-6] mi · [\d,]+ ft$/);
  const wide = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  expect(wide[0]).toBeLessThanOrEqual(wide[1]);
  const shot = info.outputPath("elevation-aa300.png");
  await page.screenshot({ path: shot });
  await info.attach("elevation-aa300", { path: shot, contentType: "image/png" });
});

test("no elevation_profile: no chart", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await openPlan(page, DEMO_PLAN);
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  await expect(page.locator("[data-elevation-chart]")).toHaveCount(0);
});
