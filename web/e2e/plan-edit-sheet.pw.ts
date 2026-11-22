import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// refs #2555: with the details sheet open over a result, Edit opens the form; the sheet steps down to its peek so the form is not under it.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
test.use({ actionTimeout: 5000 });

const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

for (const theme of ["dark", "light"] as const) {
  test(`phone ${theme}: Edit over an open details sheet leaves the form uncovered (key on)`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
    await stubApi(page, { designPreview: ["plan_v2"] });
    await page.route("**/api/settings/planner/favorites", (r) => r.fulfill(j([{ id: "f1", label: "Sample destination", latitude: 36.17, longitude: -115.14 }])));
    await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
    await page.goto("/trips/plan");
    await page.locator("[data-plan-field][data-heading='From']").getByRole("button", { name: /Home/ }).click();
    await page.locator("[data-plan-field][data-heading='To']").getByRole("button", { name: "Sample destination" }).click();
    await page.getByRole("button", { name: "Plan trip" }).click();
    const sheet = page.locator("[data-plan-sheet]");
    await expect(sheet).toBeVisible();

    // Open the details (one press from the peek), then Edit.
    await page.getByRole("button", { name: /^Trip details:/ }).click();
    await expect(sheet).not.toHaveAttribute("data-snap", "peek");
    await page.locator("[data-plan-edit]").scrollIntoViewIfNeeded();
    await page.locator("[data-plan-edit]").click();
    await expect(page.locator("[data-plan-form]")).toBeVisible();
    await expect(sheet).toHaveAttribute("data-snap", "peek");

    // The departure row is not under the sheet: what is at its centre is the form, not the sheet.
    const row = page.locator("[data-plan-when-inner]");
    await row.scrollIntoViewIfNeeded();
    const hit = await row.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
      return !!top && !top.closest("[data-plan-sheet]");
    });
    expect(hit).toBe(true);
    await page.screenshot({ path: path.join(SHOTS, `plan-edit-sheet-${theme}.png`) });

    // Back: Done folds the form again, and the sheet can be opened once more.
    await page.locator("[data-plan-edit]").click();
    await expect(page.locator("[data-plan-bar]")).toHaveAttribute("data-collapsed", "true");
    await ctx.close();
  });
}
