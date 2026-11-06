import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// A right click (or a long press) on the planner map drops a pin whose callout says "Add as a stop". The button must take a real click:
// a popup that let the click through to the map (or sat under the layer chips) made it do nothing. Click it for real, then the planner asks
// again with the stop and the stop is listed.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };

async function planned(page: Page) {
  await stubApi(page);
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  const plans: { waypoints?: { latitude?: number; longitude?: number }[] }[] = [];
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => {
    plans.push(r.request().postDataJSON());
    return r.fulfill(j(DEMO_PLAN));
  });
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  return plans;
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`@webkit iphone: Add as a stop takes a real click and plans again with the stop · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    const plans = await planned(page);
    const map = page.locator("[data-plan-area='map'] .maplibregl-map").first();
    await expect(map.locator(".maplibregl-marker").first()).toBeVisible();
    const box = (await map.boundingBox())!;
    // Away from the chips (top left) and the sheet (bottom on a phone).
    await page.mouse.click(box.x + box.width * 0.6, box.y + box.height * 0.3, { button: "right" });
    const add = page.getByRole("button", { name: "Add as a stop" });
    await expect(add).toBeVisible();
    // The button is what is under its own centre, not the chips or the map canvas.
    const c = (await add.boundingBox())!;
    // (A popup opened under the chip row is panned clear first, so wait for the button to be the element at its own centre.)
    await expect
      .poll(async () => {
        const b = (await add.boundingBox())!;
        return page.evaluate(([x, y]) => (document.elementFromPoint(x, y) as HTMLElement | null)?.className ?? "", [b.x + b.width / 2, b.y + b.height / 2]);
      })
      .toContain("drop-add-btn");
    void c;
    const before = plans.length;
    await add.click();
    await expect.poll(() => plans.length).toBe(before + 1);
    // The new request carries the stop between the origin and the destination, and the page lists it as a stop of the person's.
    expect(plans[plans.length - 1].waypoints).toHaveLength(3);
    await expect(page.getByText("A place you chose").first()).toBeAttached(); // listed under Via (inside the collapsed form on a phone)
    await ctx.close();
  });
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`@webkit iphone: a callout opened under the layer chips leaves the chips clickable · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await planned(page);
    const map = page.locator("[data-plan-area='map'] .maplibregl-map").first();
    await expect(map.locator(".maplibregl-marker").first()).toBeVisible();
    const box = (await map.boundingBox())!;
    // Just below the chip row (top left of the map), so the popup opens up under the chips.
    const row = (await page.locator("[data-map-chips]").boundingBox())!;
    await page.mouse.click(row.x + 60, row.y + row.height + 10, { button: "right" });
    void box;
    await expect(page.getByRole("button", { name: "Add as a stop" })).toBeVisible();
    const chip = page.locator("[data-map-chips]").getByRole("button", { name: "Level 2" });
    // The chip is the element at its own centre even with the popup under the row.
    await expect
      .poll(async () => {
        const c = (await chip.boundingBox())!;
        const hit = await page.evaluate(([x, y]) => !!document.elementFromPoint(x, y)?.closest("[data-map-chips]"), [c.x + c.width / 2, c.y + c.height / 2]);
        return hit;
      })
      .toBe(true);
    await chip.click();
    await ctx.close();
  });
}
