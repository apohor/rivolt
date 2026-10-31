import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// The planner map's charger chips float over its top edge: the fitted route (its
// start, stops and end markers) must clear them, on phone and desktop, both themes.
// Signed in, the chips are there (a saved trip with the stub's plan). The demo has
// no charger source, so it has no chips (nor their "no chargers here" note) at all.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const who of ["signed-in", "demo"] as const) {
  for (const theme of ["dark", "light"] as const) {
    for (const w of [390, 1280] as const) {
      test(`route clears the Chargers chips · ${who} · ${w}px · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: { width: w, height: w === 390 ? 844 : 900 }, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript(([t]) => {
          localStorage.setItem("rivolt.theme", t);
        }, [theme]);
        if (who === "demo") {
          await stubApi(page, { demo: true, signedIn: false });
          await page.goto("/demo/trips/plan");
          await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
          await expect(page.locator("[data-plan-area='map'] .maplibregl-marker").first()).toBeVisible();
          await expect(page.locator("[data-map-chips]")).toHaveCount(0);
          await expect(page.getByText(/No chargers in this area/)).toHaveCount(0);
        } else {
          await stubApi(page);
          const t = DEMO_PRESETS[0];
          await page.route("**/api/trips/saved", (r) =>
            r.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify([{ id: "st-map", name: t.name, inputs: t.inputs, plan: DEMO_PLAN, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]),
            }),
          );
          await page.goto("/trips/plan");
          await page.getByRole("tab", { name: "Saved" }).click();
          await page.getByRole("button", { name: /LA to Las Vegas/ }).click();
          await page.getByRole("tab", { name: "Planner" }).click();
          await expect(page.locator("[data-plan-chips]")).toBeVisible();
        }
        await page.waitForTimeout(1500);
        // On a phone the bottom sheet is fixed over the page's lower part: with the map
        // scrolled up under the top bar (its top at 80px), no route marker may sit under the peek.
        if (w === 390) await page.locator("[data-plan-area='map']").evaluate((el) => window.scrollBy(0, el.getBoundingClientRect().top - 80));
        await page.waitForTimeout(400);
        await page.screenshot({ path: path.join(SHOTS, `plan-map-chips-${who}-${w}-${theme}.png`) });
        const res = await page.evaluate(() => {
          // The demo has no chips: nothing for a marker to hit.
          const chips = document.querySelector("[data-plan-chips]")?.getBoundingClientRect() ?? { left: 0, right: 0, top: 0, bottom: 0 };
          const map = document.querySelector("[data-plan-area='map'] .maplibregl-map")!.getBoundingClientRect();
          const markers = [...document.querySelectorAll("[data-plan-area='map'] .maplibregl-marker")].map((m) => m.getBoundingClientRect());
          const hits = markers.filter((m) => m.left < chips.right && m.right > chips.left && m.top < chips.bottom && m.bottom > chips.top).length;
          const outside = markers.filter((m) => m.top < map.top || m.bottom > map.bottom || m.left < map.left || m.right > map.right).length;
          const sheet = document.querySelector("[data-plan-sheet]")?.getBoundingClientRect();
          const underSheet = sheet ? markers.filter((m) => m.bottom > sheet.top && m.top < sheet.bottom && m.left < sheet.right && m.right > sheet.left).length : 0;
          return { n: markers.length, hits, outside, underSheet };
        });
        expect(res.n).toBeGreaterThan(1);
        expect(res.hits).toBe(0);
        expect(res.outside).toBe(0);
        expect(res.underSheet).toBe(0);
        await ctx.close();
      });
    }
  }
}
