import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";

// /settings#lease: the Lease card sits below cards that fill in late (the vehicle profile loads its
// vehicles, the lease form its terms). The scroll must still end with the card at the top once they
// have laid out, not at wherever the first frame left it.
for (const w of [390, 1280] as const) {
  test(`#lease ends with the Lease card at the top after late layout · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 844 } });
    const page = await ctx.newPage();
    await stubApi(page, { scenario: "parked", drain: "ok" });
    const terms = { start_date: "2024-11-03", end_date: "2027-11-03", start_odometer_mi: 12, allowed_miles_total: 36000, overage_per_mile: 0.25, currency: "USD" };
    await page.route(`**/api/vehicles/${VEHICLE_ID}/lease`, async (r) => {
      await new Promise((res) => setTimeout(res, 700));
      return r.fulfill({ json: terms });
    });
    await page.route("**/api/vehicles/owned", async (r) => {
      await new Promise((res) => setTimeout(res, 500));
      return r.fulfill({ json: { vehicles: [{ id: "o1", rivian_vehicle_id: VEHICLE_ID, display_name: "Sample R1S", model: "R1S" }] } });
    });
    await page.route(`**/api/vehicles/${VEHICLE_ID}/profile`, async (r) => {
      await new Promise((res) => setTimeout(res, 900));
      return r.fulfill({ json: { tire_type: "all_season", wheel_inches: 21, accessories: [] } });
    });
    await page.goto("/settings#lease");
    const card = page.locator("#lease");
    await expect(card).toBeVisible();
    await expect(card.locator("input[type='date']").first()).toHaveValue("2024-11-03");
    await expect(page.locator("#profile select, #profile input").first()).toBeVisible();
    await page.waitForTimeout(600);
    expect((await card.boundingBox())!.y).toBeLessThan(200);
    await ctx.close();
  });
}

// #statement-email has to open the Notifications tab, where the Monthly statement card lives, and bring it into view.
test("#statement-email opens the Notifications tab with the Monthly statement card in view", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await stubApi(page, { scenario: "parked", drain: "ok" });
  await page.goto("/settings#statement-email");
  const card = page.locator("#statement-email");
  await expect(card).toBeVisible();
  await expect(card.getByLabel("Monthly statement by email")).toBeVisible();
  await page.waitForTimeout(600);
  const box = (await card.boundingBox())!;
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.y).toBeLessThan(400);
});
