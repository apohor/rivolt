import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// plan_v2, items 2 and 3. Phone: no tall empty map box before there is a route. Totals: the total time once (strip, sheet peek and the map's
// card say the same thing in the same form), with driving and charging beneath it in the strip.
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
  for (const key of [true, false]) {
    test(`phone empty state · key ${key ? "on" : "off"} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await stubApi(page, key ? { designPreview: ["plan_v2"] } : {});
      await page.goto("/trips/plan");
      await expect(page.locator("[data-plan-form]")).toBeVisible();
      const empty = page.locator("[data-plan-area='map'][data-empty='true']");
      if (key) await expect(empty).toBeHidden();
      else await expect(empty).toBeVisible();
      await page.waitForTimeout(600);
      await page.screenshot({ path: path.join(SHOTS, `item2-393-${theme}-${key ? "on" : "off"}.png`) });
      await ctx.close();
    });
  }
}

test("desktop keeps the empty map beside the form with the key on", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await stubApi(page, { designPreview: ["plan_v2"] });
  await page.goto("/trips/plan");
  await expect(page.locator("[data-plan-area='map'][data-empty='true']")).toBeVisible();
});

test("key on: one total, the same on the map card and the sheet, with driving and charging beneath", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await openSaved(page, true);
  const card = (await page.locator(".rivolt-trip-card").first().innerText()).trim();
  const peek = (await page.locator("[data-plan-sheet-title] p").innerText()).trim();
  expect(peek).toBe(card);
  const handle = page.locator("[data-plan-sheet-handle]");
  await handle.click();
  await handle.click();
  const split = page.locator("[data-time-split]");
  await expect(split).toContainText("driving");
  await expect(split).toContainText("charging");
  await expect(page.locator("[data-plan-hero]")).not.toContainText(/stop\s*25\s*min/);
  await page.waitForTimeout(500);
  await page.screenshot({ path: path.join(SHOTS, "item3-393-totals.png") });
});

test("key off: today's totals (no split, charging minutes in the Charging cell)", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await openSaved(page, false);
  const handle = page.locator("[data-plan-sheet-handle]");
  await handle.click();
  await handle.click();
  await expect(page.locator("[data-time-split]")).toHaveCount(0);
  await expect(page.locator("[data-plan-hero]")).toContainText("25");
  await expect(page.locator("[data-plan-sheet-title] p")).toContainText("driving");
});
