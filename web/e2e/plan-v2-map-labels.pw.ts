import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// plan_v2, item 4. The map carries the charge pills and numbered stops (no weather badges: the weather is in the itinerary rows); the numbers on
// the map's pins are the numbers in the itinerary.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function openSaved(page: Page, key: boolean) {
  await stubApi(page, key ? { designPreview: ["plan_v2"] } : {});
  const t = DEMO_PRESETS[0];
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: "st-v2", name: t.name, inputs: t.inputs, plan: DEMO_PLAN, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]) }),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /LA to Las Vegas/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await expect(page.locator("[data-plan-area='map'] .maplibregl-marker").first()).toBeVisible();
}

for (const theme of ["dark", "light"] as const) {
  for (const [name, w, h] of [["393", 393, 852], ["1440", 1440, 900]] as const) {
    test(`key on: numbered stops match the itinerary, no weather on the map · ${name} ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await openSaved(page, true);
      const onMap = await page.locator(".maplibregl-marker .trip-route-dot").allInnerTexts();
      const charging = onMap.filter((t) => t.trim() !== "");
      expect(charging.length).toBeGreaterThan(0);
      expect(charging).toEqual(charging.map((_, i) => String(i + 1)));
      if (w >= 1024) {
        const rows = await page.locator("[data-stop-number]").allInnerTexts();
        expect(rows).toEqual(charging);
      }
      await expect(page.locator("[data-leg-badge]")).toHaveCount(0);
      await expect(page.locator("[data-weather-chip]")).toHaveCount(0);
      await page.waitForTimeout(800);
      await page.screenshot({ path: path.join(SHOTS, `item4-${name}-${theme}-on.png`) });
      await ctx.close();
    });
  }
}

test("key on: the phone sheet lists the same numbers", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await openSaved(page, true);
  const handle = page.locator("[data-plan-sheet-handle]");
  await handle.click();
  await handle.click();
  const rows = await page.locator("[data-stop-number]").allInnerTexts();
  const onMap = (await page.locator(".maplibregl-marker .trip-route-dot").allInnerTexts()).filter((t) => t.trim() !== "");
  expect(rows.length).toBeGreaterThan(0);
  expect(rows).toEqual(onMap);
});

test("key off: bolts on the map, no numbers in the itinerary", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, false);
  await expect(page.locator("[data-stop-number]")).toHaveCount(0);
  const dots = await page.locator(".maplibregl-marker .trip-route-dot").allInnerTexts();
  expect(dots.some((t) => t.includes("⚡"))).toBe(true);
});
