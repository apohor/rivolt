import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The planner's network-rate cost is an estimate: one note on the cost strip says so (not per network, and the figures stay).
for (const next of [true, false]) {
  test(`one cost-estimate note on the cost strip · ${next ? "new look" : "classic"}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    if (next) await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
    await stubApi(page, { demo: true, signedIn: false });
    await page.goto("/demo/trips/plan");
    await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
    const note = page.locator("[data-cost-note]");
    await expect(note.first()).toHaveText("Estimate, price varies by site (rates as of mid-2025)");
    // Once per route shown, never per network or stop.
    await expect(note).toHaveCount(1);
    await ctx.close();
  });
}

test("the note shows in the shot too", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo/trips/plan");
  await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
  await expect(page.locator("[data-cost-note]")).toBeVisible();
  await page.screenshot({ path: "e2e/__screenshots__/plan-cost-note-iphone-dark.png" });
  await ctx.close();
});
