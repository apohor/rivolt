import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The planner map's charger chips float over its top edge: the fitted route (its
// start, stops and end markers) must clear them, on phone and desktop, both themes.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`route clears the Chargers chips · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w === 390 ? 844 : 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { demo: true, signedIn: false });
      await page.goto("/demo/trips/plan");
      await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      await expect(page.locator("[data-plan-chips]")).toBeVisible();
      await page.waitForTimeout(1500);
      if (w === 390) await page.locator("[data-plan-area='map']").scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `plan-map-chips-${w}-${theme}.png`) });
      const res = await page.evaluate(() => {
        const chips = document.querySelector("[data-plan-chips]")!.getBoundingClientRect();
        const map = document.querySelector("[data-plan-area='map'] .leaflet-container")!.getBoundingClientRect();
        const markers = [...document.querySelectorAll("[data-plan-area='map'] .leaflet-marker-icon")].map((m) => m.getBoundingClientRect());
        const hits = markers.filter((m) => m.left < chips.right && m.right > chips.left && m.top < chips.bottom && m.bottom > chips.top).length;
        const outside = markers.filter((m) => m.top < map.top || m.bottom > map.bottom || m.left < map.left || m.right > map.right).length;
        return { n: markers.length, hits, outside };
      });
      expect(res.n).toBeGreaterThan(1);
      expect(res.hits).toBe(0);
      expect(res.outside).toBe(0);
      await ctx.close();
    });
  }
}
