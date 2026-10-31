import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The planner's map draws the route in one colour (a plan has no per-sample metric to colour by) with a one-line card of what the plan is. Shots wait for the line
// to be drawn (data-map-drawn above zero) and are of the same trip in both themes at both widths.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const [name, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`${name === "iphone" ? "iphone: " : ""}the plan map's card says what the plan is · ${name} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await stubApi(page, { demo: true, signedIn: false });
      await page.goto("/demo/trips/plan");
      await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      const map = page.locator("[data-plan-area='map']");
      await expect(map.locator("[data-map-drawn]:not([data-map-drawn='0'])").first()).toBeAttached();
      // One line says what the plan is: its distance, its charging stop and its time (the demo's LA to Las Vegas has one stop).
      const card = map.locator(".rivolt-trip-card");
      await expect(card).toBeVisible();
      await expect(card).toContainText(/\d+ mi · 1 charging stop · \d+h/);
      // The card ends left of the credits' (i).
      const lb = (await card.boundingBox())!;
      const ib = (await map.locator(".maplibregl-ctrl-attrib-button").boundingBox())!;
      expect(lb.x + lb.width).toBeLessThanOrEqual(ib.x);
      await map.screenshot({ path: path.join(SHOTS, `plan-map-card-${name}-${theme}.png`) });
      await ctx.close();
    });
  }
}
