import fs from "node:fs";
import path from "node:path";
import { test } from "@playwright/test";
import { stubApi } from "./stub";

// Concept screenshots for the map-first planner, with the real basemap from preview.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1360, height: 1000 } },
  ] as const) {
    test(`concept ${v.name} ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { demo: true, signedIn: false, tilesUrl: "https://preview.rivolt.dev/api/maps/tiles/us.pmtiles" });
      await page.goto("/demo/trips/plan");
      await page.waitForTimeout(800);
      await page.screenshot({ path: path.join(SHOTS, `concept-empty-${v.name}-${theme}.png`) });
      await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      await page.waitForTimeout(4000);
      await page.screenshot({ path: path.join(SHOTS, `concept-${v.name}-${theme}.png`), fullPage: v.name === "iphone" });
    });
  }
}
