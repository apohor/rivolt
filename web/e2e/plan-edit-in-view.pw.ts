import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// refs #2536: after Plan, the page rests with the folded summary line and its Edit button in view (not under the top bar), and a real press on Edit opens the form.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
test.use({ actionTimeout: 5000 });

const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

for (const v of [
  { name: "phone narrow", w: 320, h: 640 },
  { name: "phone", w: 393, h: 852 },
  { name: "desktop", w: 1440, h: 960 },
]) {
  for (const theme of ["dark", "light"] as const) {
    test(`${v.name} ${v.w} ${theme}: after Plan the Edit button is in view and opens the form (key on)`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: v.w, height: v.h }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await stubApi(page, { designPreview: ["plan_v2"] });
      await page.route("**/api/settings/planner/favorites", (r) => r.fulfill(j([{ id: "f1", label: "Sample destination", latitude: 36.17, longitude: -115.14 }])));
      await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
      await page.goto("/trips/plan");
      await page.locator("[data-plan-field][data-heading='From']").getByRole("button", { name: /Home/ }).click();
      await page.locator("[data-plan-field][data-heading='To']").getByRole("button", { name: "Sample destination" }).click();
      await page.getByRole("button", { name: "Plan trip" }).click();
      await expect(page.locator("[data-plan-bar]")).toHaveAttribute("data-collapsed", "true");
      // The scroll to the result runs for up to 3 s (it re-aims while the layout settles); look after it.
      await page.waitForTimeout(3300);
      const edit = page.locator("[data-plan-edit]");
      const seen = await edit.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const header = document.querySelector("header.sticky")!.getBoundingClientRect();
        const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
        return { top: r.top, bottom: r.bottom, headerBottom: header.bottom, innerHeight, hit: !!top && el.contains(top) };
      });
      expect(seen.top, `Edit starts at ${seen.top}, the top bar ends at ${seen.headerBottom}`).toBeGreaterThanOrEqual(seen.headerBottom);
      expect(seen.bottom).toBeLessThanOrEqual(seen.innerHeight);
      expect(seen.hit, "what is at the middle of the Edit line is the Edit line").toBe(true);
      await page.screenshot({ path: path.join(SHOTS, `plan-edit-in-view-${v.w}-${theme}.png`) });
      await edit.click();
      await expect(page.locator("[data-plan-form]")).toBeVisible();
      await expect(page.locator("[data-plan-bar]")).not.toHaveAttribute("data-collapsed", "true");
      await ctx.close();
    });
  }
}
