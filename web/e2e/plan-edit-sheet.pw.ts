import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// refs #2555: with the details sheet open over a result, Edit opens the form; the sheet lowers to its handle so the whole form shows, and Done brings it back to where it was.
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
    const before = await sheet.getAttribute("data-snap");
    await page.locator("[data-plan-edit]").scrollIntoViewIfNeeded();
    await page.locator("[data-plan-edit]").click();
    await expect(page.locator("[data-plan-form]")).toBeVisible();
    await expect(sheet).toHaveAttribute("data-snap", "min");
    // The sheet slides down over a transition: measure once it has settled at the handle.
    await expect.poll(async () => (await sheet.boundingBox())?.y ?? 0).toBeGreaterThan(700);

    // Nothing of the form is under the sheet: the departure row, the starting charge and Plan trip each have the form at their centre.
    const targets = [page.locator("[data-plan-when-inner]"), page.getByRole("slider", { name: "Starting SoC" }), page.getByRole("button", { name: /Update plan|Plan trip/ })];
    for (const [i, el] of targets.entries()) {
      await el.scrollIntoViewIfNeeded();
      const hit = await el.evaluate((node) => {
        const r = node.getBoundingClientRect();
        const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return !!top && !top.closest("[data-plan-sheet]");
      });
      expect(hit, `form control ${i}`).toBe(true);
    }
    await page.screenshot({ path: path.join(SHOTS, `plan-edit-sheet-${theme}.png`) });

    // Back: Done folds the form again, and the sheet can be opened once more.
    await page.locator("[data-plan-edit]").click();
    await expect(page.locator("[data-plan-bar]")).toHaveAttribute("data-collapsed", "true");
    await expect(sheet).toHaveAttribute("data-snap", before ?? "");
    await ctx.close();
  });
}
