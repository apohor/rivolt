import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// #1981: at Safari's largest text (aA 300 %: 131 x 232 CSS px) the open sheet left its details 46 px under the title, less than one route card, so
// the route choices and the itinerary could not be reached. The open sheet now also covers the top bar and drops the title there.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Las Vegas", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

test("@webkit phone at aA 300%: the open sheet's route choices and itinerary can be reached", async ({ browser }, info) => {
  const ctx = await browser.newContext({ viewport: { width: 131, height: 232 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  // Two routes, so the route choices show.
  await page.route("**/api/trips/plan", (r) => r.fulfill(j({ ...DEMO_PLAN, Routes: [DEMO_PLAN.Routes[0], DEMO_PLAN.Routes[0]] })));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  const sheet = page.locator("[data-plan-sheet]");
  const handle = page.locator("[data-plan-sheet-handle]");
  const body = page.locator("[data-plan-sheet-body]");
  await expect(sheet).toHaveAttribute("data-snap", "peek");
  await handle.click();
  await handle.click();
  await expect(sheet).toHaveAttribute("data-snap", "full");
  await page.waitForTimeout(500);
  const shot = info.outputPath("sheet-300-full.png");
  await page.screenshot({ path: shot });
  await info.attach("sheet-300-full", { path: shot, contentType: "image/png" });

  // The details get most of the screen above the tab bar, and the handle and Map are on top, where a tap lands on them.
  const room = await body.evaluate((b) => b.clientHeight);
  expect(room, `details ${room}px`).toBeGreaterThanOrEqual(120);
  for (const sel of ["[data-plan-sheet-handle]", "[data-plan-sheet-map]"]) {
    const hit = await page.locator(sel).evaluate((el) => {
      const r = el.getBoundingClientRect();
      const x = el.matches("[data-plan-sheet-map]") ? r.left + r.width / 2 : r.left + 20;
      return el.contains(document.elementFromPoint(x, r.top + r.height / 2));
    });
    expect(hit, `${sel} is covered`).toBe(true);
  }

  // The itinerary's last row scrolls into the details' view.
  const last = page.locator("[data-plan-timeline] > li").last();
  await last.scrollIntoViewIfNeeded();
  const [b, l] = await Promise.all([body.boundingBox(), last.boundingBox()]);
  expect(l!.y).toBeGreaterThanOrEqual(b!.y - 0.5);
  expect(l!.y + Math.min(l!.height, 24)).toBeLessThanOrEqual(b!.y + b!.height + 0.5);

  // Map folds it back to the peek, and the top bar shows again.
  await page.locator("[data-plan-sheet-map]").click();
  await expect(sheet).toHaveAttribute("data-snap", "peek");
  const header = await page.evaluate(() => {
    const h = document.querySelector("header")!.getBoundingClientRect();
    return document.querySelector("header")!.contains(document.elementFromPoint(h.left + h.width / 2, h.top + h.height / 2));
  });
  expect(header).toBe(true);

  // Opened again, the second route choice is reached by scrolling the details and can be chosen.
  await handle.click();
  await handle.click();
  await expect(sheet).toHaveAttribute("data-snap", "full");
  const second = page.locator("[data-plan-routes] [role=radio]").nth(1);
  await second.click();
  await expect(second).toHaveAttribute("aria-checked", "true");
});
