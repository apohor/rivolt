import { expect, test } from "@playwright/test";
import { routeInStrip } from "./routeStrip";
import { DEMO_PLAN, stubApi } from "./stub";

// Safari's aA text at 300% lays an iPhone out 131 x 232 CSS px: the map above the sheet's peek is a strip some 60 px tall, less than its chips
// and zoom buttons. The route must still show there, framed in the strip (rc gate, Nov 12): the controls step aside and the fit uses what is left.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Las Vegas", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

test(`@webkit phone at aA 300%: the route shows in the strip of map above the sheet`, async ({ browser }, info) => {
  const ctx = await browser.newContext({ viewport: { width: 131, height: 232 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  const sheet = page.locator("[data-plan-sheet]");
  await expect(sheet).toHaveAttribute("data-snap", "peek");
  await expect(page.locator("[data-map-drawn]:not([data-map-drawn='0'])")).toBeAttached();
  // Opened to half, then back with Map: the route is framed again above the peek.
  for (const step of ["peek", "after Map"] as const) {
    if (step === "after Map") {
      await page.locator("[data-plan-sheet-handle]").click();
      await expect(sheet).toHaveAttribute("data-snap", "half");
      await page.waitForTimeout(600);
      await page.locator("[data-plan-sheet-map]").click();
      await expect(sheet).toHaveAttribute("data-snap", "peek");
    }
    // The planner scrolls the map into view and the map refits once the sheet settles (0.26 s).
    await page.waitForTimeout(1500);
    const shot = info.outputPath(`strip-300-${step.replace(" ", "-")}.png`);
    await page.screenshot({ path: shot });
    await info.attach(`strip-300-${step}`, { path: shot, contentType: "image/png" });
    // The chips and the zoom buttons are out of the strip's way.
    await expect(page.locator("[data-map-chips]")).toBeHidden();
    await expect(page.locator("[data-trip-map] .maplibregl-ctrl-zoom-in")).toBeHidden();
    const got = await routeInStrip(page);
    // Framed: the line runs across a good part of the strip, not a dot at its edge or nothing at all.
    expect(got.n, `${step}: route pixels in the ${got.w}x${got.h} strip`).toBeGreaterThan(30);
    expect(got.span, `${step}: the route spans ${got.span}px of the ${got.w}x${got.h} strip`).toBeGreaterThanOrEqual(Math.min(got.w, got.h) * 0.5);
  }
  await ctx.close();
});
