import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// plan_v2, item 6: the charger filter chips say clearly which are on (a check and a border), and an arrival charge under the person's reserve
// (their "Target arrival SoC") is tinted amber and said in words.
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

// The target field is under "More options"; with the key on the form folds once a plan shows, so Edit opens it first.
async function targetField(page: Page) {
  const edit = page.locator("[data-plan-edit]");
  if (await edit.isVisible().catch(() => false)) await edit.click();
  const field = page.getByLabel("Target arrival SoC %");
  if (!(await field.isVisible().catch(() => false))) await page.getByRole("button", { name: /More options/ }).click();
  return field;
}

for (const theme of ["dark", "light"] as const) {
  test(`chips: on has a check and a border, off is outlined · ${theme}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: theme });
    const page = await ctx.newPage();
    await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
    await openSaved(page, true);
    const chips = page.locator("[data-plan-area='map'] button[aria-pressed]").filter({ hasText: /fast|level|hotel|camp|DC|L2/i });
    const n = await chips.count();
    expect(n).toBeGreaterThan(1);
    const on = chips.locator("xpath=self::*[@aria-pressed='true']");
    const off = chips.locator("xpath=self::*[@aria-pressed='false']");
    await expect(on.first().locator("[data-chip-check]")).toBeVisible();
    await expect(off.first().locator("[data-chip-check]")).toHaveCount(0);
    const bw = async (l: typeof on) => l.first().evaluate((e) => getComputedStyle(e).borderTopWidth);
    expect(await bw(on)).toBe("1px");
    expect(await bw(off)).toBe("1px");
    await page.waitForTimeout(500);
    await page.screenshot({ path: path.join(SHOTS, `item6-chips-${theme}.png`) });
    await ctx.close();
  });
}

test("key off: chips are today's, no check", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, false);
  await expect(page.locator("[data-chip-check]")).toHaveCount(0);
});

test("reserve: an arrival under it is tinted, over it is not, key off never", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, true);
  const target = await targetField(page);
  await target.fill("1");
  await expect(page.locator("[data-under-reserve]")).toHaveCount(0);
  await target.fill("");
  await expect(page.locator("[data-under-reserve]")).toHaveCount(0);
  await target.fill("99");
  await expect(page.locator("[data-plan-hero] [data-under-reserve]")).toBeVisible();
  await expect(page.locator("[data-plan-hero] svg[role='img']")).toHaveAttribute("aria-label", /under your 99 percent reserve/);
  await expect(page.locator("[data-plan-timeline] [data-under-reserve]")).toContainText("%");
  await page.getByRole("button", { name: "Done" }).click();
  await page.waitForTimeout(500);
  await page.screenshot({ path: path.join(SHOTS, "item6-reserve-under.png") });
});

test("reserve, key off: no tint", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, false);
  await (await targetField(page)).fill("99");
  await expect(page.locator("[data-under-reserve]")).toHaveCount(0);
});
