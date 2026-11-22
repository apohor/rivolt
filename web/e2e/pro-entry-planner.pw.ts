import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Design key pro_entry (D5 A): when a free account presses Plan trip and the server answers 402 pro_required, the note says what needs the
// Pro tier and how to keep the plan free; key off it is today's note. Stills are written for a person to open.
const SHOTS = process.env.PRO_ENTRY_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const fixture = (n: string) => JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", n), "utf8")) as Record<string, unknown>;
const free = fixture("me_plan_free.json");
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Current vehicle position", 34.05, -118.24), destination: pt("Sacramento, California, United States", 38.58, -121.49), extra_stops: [], overnight_flags: [], starting_soc: "74", target_soc: "20" };
const j = (status: number, b: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(b) });

async function open(page: Page, keys: string[]) {
  await stubApi(page, { designPreview: keys });
  await page.route("**/api/auth/me", (r) => r.fulfill(j(200, free)));
  await page.route("**/api/trips/saved", (r) => r.fulfill(j(200, [{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(402, { error: "pro_required" })));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  return page.locator("[data-pro-limit]");
}

for (const theme of ["light", "dark"] as const) {
  for (const w of [393, 1440] as const) {
    test(`pro_entry planner note @webkit · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w < 500 ? 852 : 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);

      // Key on: the reworded note and "See the Pro tier"; the button opens the sheet and Escape comes back to the note.
      const on = await open(page, ["pro_entry"]);
      await expect(on).toContainText("Plans with an overnight are in the Pro tier. A single-day plan stays free: remove the overnight and plan again.");
      await expect(on).not.toContainText("Rivolt Pro");
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      await on.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `pro-entry-planner-on-${w}-${theme}.png`) });
      await on.getByRole("button", { name: "See the Pro tier" }).click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await expect(on).toBeVisible();

      // Key off: today's note and "See Pro".
      const off = await open(page, []);
      await expect(off).toContainText("Multi-day plans, overnights and re-plan from here are part of Rivolt Pro. A single-day plan stays free.");
      await expect(off.getByRole("button", { name: "See Pro" })).toBeVisible();
      await off.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(SHOTS, `pro-entry-planner-off-${w}-${theme}.png`) });
      await ctx.close();
    });
  }
}
