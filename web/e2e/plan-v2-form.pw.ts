import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// plan_v2, item 5 (form polish): line icons in place of emoji, no chip for the place that is already the other end, the "N changed" badge,
// the overnight explainer in one short line, and departure chips that never leave one alone on a line at 393 px.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function open(page: Page, key: boolean) {
  await stubApi(page, key ? { designPreview: ["plan_v2"] } : {});
  await page.route("**/api/settings/planner/favorites", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: "f1", label: "CB", latitude: 34.5, longitude: -117.3 }]) }),
  );
  await page.goto("/trips/plan");
  await expect(page.locator("[data-plan-form]")).toBeVisible();
}

for (const theme of ["dark", "light"] as const) {
  for (const key of [true, false]) {
    test(`form · key ${key ? "on" : "off"} · 393 ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page, key);
      const from = page.locator("[data-plan-field][data-heading='From']");
      const to = page.locator("[data-plan-field][data-heading='To']");
      await expect(from.getByRole("button", { name: /Home/ })).toBeVisible();
      await from.getByRole("button", { name: /Home/ }).click();
      // The saved place keeps the person's own name; the other end no longer offers Home while the trip starts there.
      await expect(to.getByRole("button", { name: "CB" })).toBeVisible();
      if (key) await expect(to.getByRole("button", { name: /Home/ })).toHaveCount(0);
      else await expect(to.getByRole("button", { name: /Home/ })).toBeVisible();
      const when = page.locator("[data-plan-when-inner]");
      const text = await when.innerText();
      if (key) {
        expect(text).not.toMatch(/📅|🕐/);
        expect(await when.locator("svg").count()).toBe(2);
        const picks = await page.locator("[data-plan-when-picks] button").evaluateAll((bs) => bs.map((b) => Math.round(b.getBoundingClientRect().top)));
        expect(new Set(picks).size).toBe(1);
        const exact = await page.locator("[data-plan-when-exact] button").evaluateAll((bs) => bs.map((b) => Math.round(b.getBoundingClientRect().top)));
        expect(exact.length).toBe(2);
        expect(new Set(exact).size).toBe(1);
        await expect(page.locator("[data-plan-overnights] p")).toHaveText("More than a day? Add a night and each day is planned.");
      } else {
        expect(text).toMatch(/📅/);
        await expect(page.locator("[data-plan-overnights] p")).toContainText("Going further than a day?");
      }
      await page.waitForTimeout(300);
      await page.screenshot({ path: path.join(SHOTS, `item5-393-${theme}-${key ? "on" : "off"}.png`) });
      await ctx.close();
    });
  }
}

test("key on: Save place is quiet until hover or focus", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await open(page, true);
  await page.locator("[data-plan-field][data-heading='From']").getByRole("button", { name: "CB" }).click();
  const save = page.getByRole("button", { name: /Save place/ });
  await expect(save).toHaveClass(/text-neutral-500/);
  await expect(save).toHaveClass(/hover:text-amber-300/);
});
