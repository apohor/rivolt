import { expect, test } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// The itinerary's "FAST · 200 KW" badge is one line, not "FAST ·" over "200 KW", at phone widths up to 393 px.
for (const w of [320, 360, 393]) {
  test(`charger badge stays on one line · ${w}px`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: 852 });
    await stubApi(page);
    const t = DEMO_PRESETS[0];
    await page.route("**/api/trips/saved", (r) =>
      r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: "st-b", name: t.name, inputs: t.inputs, plan: DEMO_PLAN, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]) }),
    );
    await page.goto("/trips/plan");
    await page.getByRole("tab", { name: "Saved" }).click();
    await page.getByRole("button", { name: /LA to Las Vegas/ }).click();
    await page.getByRole("tab", { name: "Planner" }).click();
    await expect(page.locator("[data-plan-sheet]")).toBeVisible();
    const handle = page.locator("[data-plan-sheet-handle]");
    await handle.click();
    await handle.click();
    await expect(page.locator("[data-plan-sheet]")).toHaveAttribute("data-snap", "full");
    const badge = page.locator("[data-charger-type]").first();
    await expect(badge).toBeVisible();
    const lines = await badge.evaluate((el) => el.getClientRects().length);
    const h = (await badge.boundingBox())!.height;
    expect(lines).toBe(1);
    expect(h).toBeLessThan(24);
  });
}
