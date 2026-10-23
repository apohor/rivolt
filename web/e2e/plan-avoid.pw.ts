import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Avoid tolls / ferries / highways under More options: restored from a saved
// trip, counted in the badge, and sent as route_preferences only when on.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const SAVED = (extra: object) => [
  {
    id: "st-avoid",
    name: "Avoid test",
    inputs: { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], ...extra },
    created_at: "2026-10-17T00:00:00Z",
    updated_at: "2026-10-17T00:00:00Z",
  },
];

async function open(page: Page, inputs: object) {
  await stubApi(page);
  await page.route("**/api/trips/saved", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(SAVED(inputs)) }));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Avoid test/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.locator("[data-plan-more-toggle]").click();
}

async function planBody(page: Page) {
  const req = page.waitForRequest("**/api/trips/plan");
  await page.getByRole("button", { name: "Plan trip" }).click();
  return (await req).postDataJSON() as { route_preferences?: Record<string, boolean> };
}

test("avoid prefs restore from a saved trip, show in the badge, and are sent", async ({ page }) => {
  await open(page, { route_preferences: { avoid_tolls: true, avoid_ferries: false, avoid_highways: true } });
  const group = page.locator("[data-avoid-toggles]");
  await expect(group.getByLabel("Tolls")).toBeChecked();
  await expect(group.getByLabel("Ferries")).not.toBeChecked();
  await expect(group.getByLabel("Highways")).toBeChecked();
  await expect(page.locator("[data-plan-more-count]")).toHaveAttribute("aria-label", "2 changed");
  const body = await planBody(page);
  expect(body.route_preferences).toEqual({ avoid_tolls: true, avoid_ferries: false, avoid_highways: true });
  const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).exclude(".leaflet-container").analyze();
  expect(res.violations.map((v) => v.id)).toEqual([]);
});

test("with nothing avoided the request has no route_preferences", async ({ page }) => {
  await open(page, {});
  await expect(page.locator("[data-plan-more-count]")).toHaveCount(0);
  const body = await planBody(page);
  expect(body.route_preferences).toBeUndefined();
});
