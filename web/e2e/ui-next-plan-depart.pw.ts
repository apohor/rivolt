import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// A single-day plan is for the date and time picked in the route bar. It used to leave out depart_at, so a plan for
// Dec 15 was really for now and the far-future note never showed.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = (departure_at: string) => ({ origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [], departure_at });

async function open(page: Page, departure_at: string, withPlan = false) {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  const bodies: Record<string, unknown>[] = [];
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS(departure_at), ...(withPlan ? { plan: DEMO_PLAN } : {}), created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])),
  );
  await page.route("**/api/trips/plan", (r) => {
    bodies.push(r.request().postDataJSON() as Record<string, unknown>);
    return r.fulfill(j({ ...DEMO_PLAN, far_future: true }));
  });
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  return bodies;
}

test("the picked date and time go to the planner, and a far-off departure shows the note", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark", timezoneId: "America/Los_Angeles" });
  const page = await ctx.newPage();
  const bodies = await open(page, "2026-12-15T08:00");
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect.poll(() => bodies.length).toBe(1);
  // 8:00 AM in Los Angeles on Dec 15 (PST, UTC-8).
  expect(bodies[0].depart_at).toBe("2026-12-15T16:00:00.000Z");
  await expect(page.locator("[data-far-future-note]")).toBeVisible();
  await ctx.close();
});

test("leaving now sends no depart_at", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  const bodies = await open(page, "");
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect.poll(() => bodies.length).toBe(1);
  expect(bodies[0]).not.toHaveProperty("depart_at");
  await ctx.close();
});

test("re-planning a saved single-day trip keeps its departure date", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark", timezoneId: "America/Los_Angeles" });
  const page = await ctx.newPage();
  const bodies = await open(page, "2026-12-15T08:00", true);
  await page.getByRole("button", { name: "Re-plan now" }).click();
  await expect.poll(() => bodies.length).toBe(1);
  expect(bodies[0].depart_at).toBe("2026-12-15T16:00:00.000Z");
  await ctx.close();
});
