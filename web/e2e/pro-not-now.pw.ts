import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Design key pro_entry (D6): "Not now" on the planner note and on the lapse banner is kept per account on the server. The stub keeps that
// list the way the server does, so a reload shows what a second visit would. Stills are written for a person to open.
const SHOTS = process.env.PRO_ENTRY_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const fixture = (n: string) => JSON.parse(fs.readFileSync(path.join(process.cwd(), "..", "ios", "RivoltTests", "Fixtures", n), "utf8")) as Record<string, unknown>;
const free = fixture("me_plan_free.json");
const lapsed = fixture("me_plan_lapsed.json");
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const NIGHT = { origin: pt("Current vehicle position", 34.05, -118.24), destination: pt("Sacramento, California, United States", 38.58, -121.49), extra_stops: [pt("Reno, Nevada, United States", 39.53, -119.81)], overnight_flags: [true], starting_soc: "74", target_soc: "20" };
const j = (status: number, b: unknown) => ({ status, contentType: "application/json", body: JSON.stringify(b) });

async function setup(page: Page, keys: string[], me: Record<string, unknown>, hidden: Set<string>, posts: string[]) {
  await stubApi(page, { designPreview: keys });
  await page.route("**/api/auth/me", (r) => r.fulfill(j(200, { ...me, ...(hidden.size ? { pro_prompts_hidden: [...hidden] } : {}) })));
  await page.route("**/api/pro/prompts/*/dismiss", (r) => {
    const where = r.request().url().split("/pro/prompts/")[1].split("/")[0];
    posts.push(where);
    hidden.add(where);
    return r.fulfill({ status: 204 });
  });
  await page.route("**/api/trips/saved", (r) => r.fulfill(j(200, [{ id: "st-a", name: "Old trip", inputs: NIGHT, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan-multiday", (r) => r.fulfill(j(402, { error: "pro_required" })));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(402, { error: "pro_required" })));
}
async function plan(page: Page) {
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: /^Plan (2-day )?trip$/ }).click();
  return page.locator("[data-pro-limit]");
}
const shot = async (page: Page, name: string) => {
  await page.evaluate(() => window.scrollBy(0, 140));
  await page.screenshot({ path: path.join(SHOTS, name) });
};

for (const theme of ["light", "dark"] as const) {
  for (const w of [320, 393, 1440] as const) {
    test(`pro_entry Not now @webkit · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w < 500 ? 852 : 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);

      // Planner note: no demo link; Not now removes both buttons, the reason line stays, and it stays so after a reload.
      const hidden = new Set<string>();
      const posts: string[] = [];
      await setup(page, ["pro_entry"], free, hidden, posts);
      let note = await plan(page);
      await expect(note.getByRole("button", { name: "See the Pro tier" })).toBeVisible();
      await expect(note.getByRole("link", { name: /in the demo/ })).toHaveCount(0);
      await note.scrollIntoViewIfNeeded();
      await shot(page, `pro-not-now-planner-before-${w}-${theme}.png`);
      await note.getByRole("button", { name: "Not now" }).click();
      await expect(note.getByRole("button")).toHaveCount(0);
      await expect(note).toContainText("Plans with an overnight are in the Pro tier");
      await expect.poll(() => posts).toEqual(["planner"]);
      await shot(page, `pro-not-now-planner-after-${w}-${theme}.png`);
      await page.reload();
      note = await plan(page);
      await expect(note).toContainText("Plans with an overnight are in the Pro tier");
      await expect(note.getByRole("button")).toHaveCount(0);
      expect(posts).toEqual(["planner"]);

      // Lapse banner: its button reads "Not now"; it hides the banner and stays hidden after a reload.
      const h2 = new Set<string>();
      const p2: string[] = [];
      const page2 = await ctx.newPage();
      await setup(page2, ["pro_entry"], lapsed, h2, p2);
      await page2.goto("/");
      const banner = page2.locator("[data-plan-notice]");
      await expect(banner.getByRole("button", { name: "Dismiss" })).toHaveCount(0);
      await banner.scrollIntoViewIfNeeded();
      await page2.screenshot({ path: path.join(SHOTS, `pro-not-now-banner-before-${w}-${theme}.png`) });
      await banner.getByRole("button", { name: "Not now" }).click();
      await expect(banner).toHaveCount(0);
      await expect.poll(() => p2).toEqual(["lapse_banner"]);
      await page2.screenshot({ path: path.join(SHOTS, `pro-not-now-banner-after-${w}-${theme}.png`) });
      await page2.reload();
      await expect(page2.getByRole("link", { name: "Settings" }).first()).toBeVisible();
      await expect(page2.locator("[data-plan-notice]")).toHaveCount(0);
      expect(p2).toEqual(["lapse_banner"]);

      // Key off: today's banner, the browser keeps the dismissal, no request to the store.
      const page3 = await ctx.newPage();
      const p3: string[] = [];
      await setup(page3, [], lapsed, new Set(), p3);
      await page3.goto("/");
      await page3.locator("[data-plan-notice]").getByRole("button", { name: "Dismiss" }).click();
      await expect(page3.locator("[data-plan-notice]")).toHaveCount(0);
      expect(p3).toEqual([]);
      await ctx.close();
    });
  }
}
