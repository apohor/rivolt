import { expect, test, type Locator, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// The planner's sheet on a phone, dragged (a mouse here; the same code path as a finger, which qa checks on the simulator): a medium flick from the
// peek opens half, not the details; a drag past half opens the details; at full a drag down in the details scrolls them until they are at their top,
// and only then moves the sheet; a sideways drag leaves it; a resize keeps the height it is at.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

async function open(page: Page) {
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
  await held(sheet);
  return sheet;
}

// The sheet's height animates for a moment after a change: wait until it holds still.
async function held(sheet: Locator) {
  let last = -1;
  let still = 0;
  await expect.poll(async () => {
    const h = (await sheet.boundingBox())!.height;
    still = h === last ? still + 1 : 0;
    last = h;
    return still;
  }, { intervals: [100] }).toBeGreaterThanOrEqual(3);
}

// Drags from (x, y) by (dx, dy) in n moves, pausing `wait` ms between them (0: a flick).
async function drag(page: Page, x: number, y: number, dx: number, dy: number, n: number, wait: number) {
  await page.mouse.move(x, y);
  await page.mouse.down();
  for (let i = 1; i <= n; i++) {
    await page.mouse.move(x + (dx * i) / n, y + (dy * i) / n);
    if (wait) await page.waitForTimeout(wait);
  }
  await page.mouse.up();
}

const title = async (page: Page) => {
  const b = (await page.locator("[data-plan-sheet-title]").boundingBox())!;
  return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
};

test("@webkit iphone: sheet drags settle by distance and speed", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const sheet = await open(page);

  // A sideways drag does nothing.
  let t = await title(page);
  await drag(page, t.x, t.y, 120, -8, 6, 0);
  await expect(sheet).toHaveAttribute("data-snap", "peek");

  // A medium flick up from the peek opens half, not the details (the speed rule itself is in BottomSheet.test.tsx).
  await drag(page, t.x, t.y, 0, -150, 3, 0);
  await expect(sheet).toHaveAttribute("data-snap", "half");
  await page.getByRole("button", { name: "Map" }).click();
  await expect(sheet).toHaveAttribute("data-snap", "peek");
  await held(sheet);

  // A slow drag well past half opens the details.
  t = await title(page);
  await drag(page, t.x, t.y, 0, -520, 26, 30);
  await expect(sheet).toHaveAttribute("data-snap", "full");
  await held(sheet);

  // At full, with the details scrolled, a drag down in them scrolls them, not the sheet.
  const body = page.locator("[data-plan-sheet-body]");
  await body.evaluate((el) => (el.scrollTop = 200));
  const bb = (await body.boundingBox())!;
  await drag(page, bb.x + bb.width / 2, bb.y + 200, 0, 120, 12, 30);
  await expect(sheet).toHaveAttribute("data-snap", "full");
  // At their top, a drag down moves the sheet.
  await body.evaluate((el) => (el.scrollTop = 0));
  await drag(page, bb.x + bb.width / 2, bb.y + 100, 0, 300, 15, 30);
  await expect(sheet).not.toHaveAttribute("data-snap", "full");

  // A resize keeps the height it is at.
  await page.locator("[data-plan-sheet-handle]").press("End");
  await expect(sheet).toHaveAttribute("data-snap", "full");
  await page.setViewportSize({ width: 390, height: 700 });
  await page.waitForTimeout(300);
  await expect(sheet).toHaveAttribute("data-snap", "full");
  const box = (await sheet.boundingBox())!;
  expect(box.y + box.height).toBeLessThanOrEqual(700);
  await ctx.close();
});
