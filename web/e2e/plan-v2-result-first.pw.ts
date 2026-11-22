import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// plan_v2, item 1: once a plan is shown the form folds to one summary line (route, departure, starting charge) with an
// Edit control, at phone and desktop widths; with the key off the desktop form stays open beside the result.
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
  for (const w of [393, 1440] as const) {
    test(`key on: form folds to a summary line, Edit opens it · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w === 393 ? 852 : 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await openSaved(page, true);
      const bar = page.locator("[data-plan-bar]");
      const edit = bar.locator("[data-plan-edit]");
      await expect(edit).toBeVisible();
      await expect(edit).toContainText("Edit");
      await expect(edit).toContainText(/→/);
      await expect(edit).toContainText(/Leaves now|AM|PM/);
      await expect(page.getByRole("button", { name: /^(Plan trip|Update plan)$/ })).toBeHidden();
      await page.waitForTimeout(800);
      await page.screenshot({ path: path.join(SHOTS, `item1-${w}-${theme}-collapsed.png`) });
      await edit.click();
      await expect(page.getByRole("button", { name: "Update plan" })).toBeVisible();
      await expect(edit).toContainText("Done");
      await edit.click();
      await expect(page.getByRole("button", { name: "Update plan" })).toBeHidden();
      await ctx.close();
    });
  }
}

test("key on: Save trip follows the itinerary", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, true);
  const itin = await page.locator("[data-plan-area='itinerary']").boundingBox();
  const save = await page.locator("[data-save-trip]").boundingBox();
  expect(itin && save && save.y > itin.y).toBe(true);
});

for (const w of [393, 1440] as const) {
  test(`key off: today's form (desktop stays open, Plan trip) · ${w}px`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: 900 });
    await openSaved(page, false);
    if (w === 1440) {
      await expect(page.locator("[data-plan-edit]")).toBeHidden();
      await expect(page.getByRole("button", { name: "Plan trip" })).toBeVisible();
    } else {
      await expect(page.locator("[data-plan-edit]")).toContainText(/→/);
      await expect(page.locator("[data-plan-edit]")).not.toContainText(/Leaves now/);
    }
    const itin = await page.locator("[data-plan-area='itinerary']").boundingBox();
    const save = await page.locator("[data-save-trip]").boundingBox();
    expect(itin && save && save.y < itin.y).toBe(true);
  });
}
