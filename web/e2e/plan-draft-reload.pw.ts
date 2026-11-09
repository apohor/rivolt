import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// A tab that asset-guard reloads (a JS chunk went missing in a deploy) keeps what was typed in the planner: the form is saved on
// "rivolt:before-reload" and taken back on the next load. Nothing was planned before, so the last-trip memory cannot be what restores it.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

test("the planner form survives an asset-guard reload", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: /More options/ }).click();
  await page.getByLabel("Target arrival SoC %").fill("33");

  // A lazy chunk fails to load: asset-guard reloads the tab after 1.5 s.
  await Promise.all([page.waitForEvent("load"), page.evaluate(() => window.dispatchEvent(new Event("vite:preloadError", { cancelable: true })))]);
  await page.getByRole("button", { name: /More options/ }).click();
  await expect(page.getByLabel("Target arrival SoC %")).toHaveValue("33");
  await expect(page.getByText("Chicago").first()).toBeVisible();
  await expect(page.getByText("Denver").first()).toBeVisible();
  // Taken back once: a second reload starts from the usual defaults.
  expect(await page.evaluate(() => window.sessionStorage.getItem("rivolt.plannerDraft"))).toBeNull();
  await ctx.close();
});
