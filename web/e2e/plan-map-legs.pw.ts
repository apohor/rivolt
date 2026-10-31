import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The planner's map colours the route by leg (the stretch between two charging stops) and shows a legend card of the legs. A plan has no per-sample
// metric, so the colour says where the stops split the trip. The demo's LA to Las Vegas preset has one stop, so two legs. Shots wait for the line
// to be drawn (data-map-drawn above zero) and are of the same trip in both themes at both widths.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const [name, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`${name === "iphone" ? "iphone: " : ""}the plan map's legend names the legs · ${name} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await stubApi(page, { demo: true, signedIn: false });
      await page.goto("/demo/trips/plan");
      await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      const map = page.locator("[data-plan-area='map']");
      await expect(map.locator("[data-map-drawn]:not([data-map-drawn='0'])").first()).toBeAttached();
      const legend = map.locator(".rivolt-leg-legend");
      await expect(legend).toBeVisible();
      await expect(legend).toContainText("Route by stop");
      // One segment per leg: origin, the Barstow stop, the destination.
      await expect(legend.locator("span[title^='Leg ']")).toHaveCount(2);
      // The card ends left of the credits' (i).
      const lb = (await legend.boundingBox())!;
      const ib = (await map.locator(".maplibregl-ctrl-attrib-button").boundingBox())!;
      expect(lb.x + lb.width).toBeLessThanOrEqual(ib.x);
      await map.screenshot({ path: path.join(SHOTS, `plan-map-legs-${name}-${theme}.png`) });
      await ctx.close();
    });
  }
}
