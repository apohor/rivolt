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

// After a plan the page scrolls the map into view smoothly (on a phone), and the map's box moves until that ends. A click aimed with a
// box read mid-scroll lands on the header or the sheet and opens no callout, which is why this passed only on a retry. Wait until the
// scroll position has held still, then aim.
async function settled(page: Page) {
  let last = -1;
  let still = 0;
  await expect
    .poll(async () => {
      const y = await page.evaluate(() => window.scrollY);
      still = y === last ? still + 1 : 0;
      last = y;
      return still;
    }, { intervals: [100] })
    .toBeGreaterThanOrEqual(3);
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
    await settled(page);
    const box = (await map.boundingBox())!;
    // Away from the chips (top left) and the sheet (bottom on a phone).
    const add = page.getByRole("button", { name: "Add as a stop" });
    // On a loaded CI WebKit runner the first right click has sometimes opened no callout (#1000; not reproducible locally under load, and the failure
    // artifact only has the page snapshot). Click again until it does, and record that the first one missed, with a screenshot, so the report counts it.
    let missed = 0;
    await expect(async () => {
      // Measure the map again on every try: a layout shift after the first measure would send a click to the wrong element.
      const now = (await map.boundingBox())!;
      const x = now.x + now.width * 0.6;
      const y = now.y + now.height * 0.3;
      await page.mouse.click(x, y, { button: "right" });
      try {
        await expect(add).toBeVisible({ timeout: 1500 });
      } catch (e) {
        if (missed++ === 0) {
          const diag = await page.evaluate(([px, py]) => {
            const el = document.elementFromPoint(px, py) as HTMLElement | null;
            return { under: el ? `${el.tagName.toLowerCase()}.${String(el.className).slice(0, 40)}` : null, scrollY: Math.round(window.scrollY), popups: document.querySelectorAll(".maplibregl-popup").length };
          }, [x, y]);
          test.info().annotations.push({
            type: "first right click opened no callout",
            description: `${v.n}; point ${Math.round(x)},${Math.round(y)} under ${diag.under}; map moved ${Math.round(now.x - box.x)},${Math.round(now.y - box.y)} since measured; scrollY ${diag.scrollY}; popups ${diag.popups}`,
          });
          await test.info().attach("first-miss", { body: await page.screenshot(), contentType: "image/png" });
        }
        throw e;
      }
    }).toPass({ timeout: 10_000 });
    // The button is what is under its own centre, not the chips or the map canvas.
    const c = (await add.boundingBox())!;
    const hit = await page.evaluate(([x, y]) => (document.elementFromPoint(x, y) as HTMLElement | null)?.className ?? "", [c.x + c.width / 2, c.y + c.height / 2]);
    expect(hit).toContain("drop-add-btn");
    const before = plans.length;
    await add.click();
    await expect.poll(() => plans.length).toBe(before + 1);
    // The new request carries the stop between the origin and the destination, and the page lists it as a stop of the person's.
    expect(plans[plans.length - 1].waypoints).toHaveLength(3);
    await expect(page.getByText("A place you chose").first()).toBeAttached(); // listed under Via (inside the collapsed form on a phone)
    await ctx.close();
  });
}

// A trip with a night in it plans as multi-day, so a stop added on the map re-plans it as multi-day too, not as one day that drops the
// night. A saved trip whose stored plan is one day while its inputs already hold a night shows the one-day map with the night set.
test("Add as a stop on a trip with a night re-plans it as multi-day", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  const single: unknown[] = [];
  const multi: { days?: unknown[] }[] = [];
  const inputs = { ...INPUTS, extra_stops: [pt("Omaha", 41.26, -95.94)], overnight_flags: [true] };
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-n", name: "Night trip", inputs, plan: DEMO_PLAN, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => {
    single.push(r.request().postDataJSON());
    return r.fulfill(j(DEMO_PLAN));
  });
  await page.route("**/api/trips/plan-multiday", (r) => {
    multi.push(r.request().postDataJSON());
    return r.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "stub" }) });
  });
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Night trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  const map = page.locator("[data-plan-area='map'] .maplibregl-map").first();
  await expect(map.locator(".maplibregl-marker").first()).toBeVisible();
  await settled(page);
  const add = page.getByRole("button", { name: "Add as a stop" });
  await expect(async () => {
    const b = (await map.boundingBox())!;
    await page.mouse.click(b.x + b.width * 0.6, b.y + b.height * 0.3, { button: "right" });
    await expect(add).toBeVisible({ timeout: 1500 });
  }).toPass({ timeout: 10_000 });
  await add.click();
  await expect.poll(() => multi.length).toBe(1);
  // Two days, split at the night, and no one-day plan.
  expect(multi[0].days).toHaveLength(2);
  expect(JSON.stringify(multi[0])).toContain("Omaha");
  expect(single).toHaveLength(0);
  // The multi-day answer fails here: the failure is said, and both stops stay on the trip.
  await expect(page.locator("[data-plan-bar] [role=alert]")).toBeVisible();
  await expect(page.getByText("A place you chose").first()).toBeAttached();
  await ctx.close();
});
