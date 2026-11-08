import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// On a phone the planner's bottom sheet: a tap on its handle reaches every height (peek, half, full, back to peek). Half shows little of a long itinerary,
// and a drag is not the only way to get past it.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };

test("@webkit iphone: tapping the sheet handle cycles peek, half, full and back", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 664 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  const sheet = page.locator("[data-plan-sheet]");
  await expect(sheet).toHaveAttribute("data-snap", "peek");
  const handle = page.locator("[data-plan-sheet-handle]");
  // The height animates for a moment after a tap: read it once it has held still.
  const settledHeight = async () => {
    let last = -1;
    let still = 0;
    await expect.poll(async () => {
      const h = (await sheet.boundingBox())!.height;
      still = h === last ? still + 1 : 0;
      last = h;
      return still;
    }, { intervals: [100] }).toBeGreaterThanOrEqual(3);
    return last;
  };
  const peekH = await settledHeight();
  const heights: number[] = [peekH];
  for (const snap of ["half", "full", "peek"] as const) {
    await handle.click();
    await expect(sheet).toHaveAttribute("data-snap", snap);
    heights.push(await settledHeight());
  }
  // half is taller than the peek, full taller than half, and a last tap is back to the peek height.
  const [peek, half, full, back] = heights;
  expect(half).toBeGreaterThan(peek);
  expect(full).toBeGreaterThan(half);
  expect(back).toBe(peek);
  await ctx.close();
});
