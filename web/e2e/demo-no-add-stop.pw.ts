import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The signed-out demo can't re-plan (that is a Rivian call), so it offers no way to add a stop: a right click on the planner map opens no
// "Add as a stop" callout and nothing is posted to /api/trips/plan.
test("the demo planner map offers no Add as a stop", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page, { demo: true, signedIn: false });
  const posts: string[] = [];
  page.on("request", (r) => { if (r.method() === "POST") posts.push(new URL(r.url()).pathname); });
  await page.goto("/demo/trips/plan");
  await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
  const map = page.locator("[data-plan-area='map'] .maplibregl-map").first();
  await expect(map.locator(".maplibregl-marker").first()).toBeVisible();
  // A stop's callout still opens (so a popup would show if one were offered), and it has no Add button.
  await map.locator(".maplibregl-marker").nth(1).click();
  await expect(page.locator(".maplibregl-popup")).toBeVisible();
  await expect(page.getByRole("button", { name: "Add as a stop" })).toHaveCount(0);
  await page.keyboard.press("Escape");
  const box = (await map.boundingBox())!;
  await page.mouse.click(box.x + box.width * 0.6, box.y + box.height * 0.3, { button: "right" });
  // Nothing to wait for, so give the (absent) popup a moment to show up.
  await page.waitForTimeout(500);
  await expect(page.getByRole("button", { name: "Add as a stop" })).toHaveCount(0);
  expect(posts.filter((p) => p === "/api/trips/plan")).toEqual([]);
  await ctx.close();
});
