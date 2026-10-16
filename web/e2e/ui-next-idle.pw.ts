import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// ui_theme_next: Now drops the idle-awake chart for a compact tile (today, average,
// 7-day sparkline) linking to Truck's full sleep chart; the wake warning stays.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    for (const state of ["off", "on"] as const) {
      test(`idle tile ${state} · ${v.name} · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript(([s, t]) => {
          localStorage.setItem("rivolt.ui.next", s);
          localStorage.setItem("rivolt.theme", t);
        }, [state, theme]);
        await stubApi(page, { scenario: "parked", drain: "ok" });
        await page.goto("/");
        await expect(page.getByRole("region", { name: "Now" })).toBeVisible();
        await page.waitForTimeout(1200);
        if (state === "off") {
          await expect(page.getByTestId("idle-tile")).toHaveCount(0);
          await expect(page.getByRole("heading", { name: "Idle-awake time per day" })).toBeVisible();
        } else {
          const tile = page.getByTestId("idle-tile");
          await expect(tile).toBeVisible();
          await expect(tile).toContainText("Idle awake");
          await expect(tile).toContainText(/avg .* a day/);
          await expect(page.getByRole("heading", { name: "Idle-awake time per day" })).toHaveCount(0);
          await expect(page.getByRole("status").filter({ hasText: "Not staying asleep" })).toBeVisible();
        }
        await page.getByTestId("idle-tile").or(page.getByText("Idle-awake time per day").first()).first().scrollIntoViewIfNeeded();
        await page.screenshot({ path: path.join(SHOTS, `idle-${v.name}-${theme}-${state}.png`), fullPage: true });
        if (state === "on") {
          const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
          const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
          expect(summary, summary.join("\n")).toEqual([]);
        }
        await ctx.close();
      });
    }
  }
}

test("the tile opens Truck's sleep card", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page, { scenario: "parked", drain: "ok" });
  await page.goto("/");
  await page.getByTestId("idle-tile").click();
  await expect(page).toHaveURL(/\/truck#sleep$/);
  await expect(page.locator("#sleep").getByText("Sleep and idle-awake")).toBeVisible();
});
