import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Theme option (System / Light / Dark): the choice beats the OS, is kept
// per device, and survives a reload without a flash (theme-init.js).
// The header switch and the "Auto" name are behind the design_preview key theme_switch (preview only).

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const html = (page: import("@playwright/test").Page) => page.locator("html");

for (const [os, choice, expectLight] of [
  ["light", "dark", false],
  ["dark", "light", true],
  ["light", "system", true],
  ["dark", "system", false],
] as const) {
  test(`settings · OS ${os} · choice ${choice}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: os });
    const page = await ctx.newPage();
    await stubApi(page, { designPreview: ["theme_switch"] });
    if (choice !== "system") {
      await page.addInitScript((c) => localStorage.setItem("rivolt.theme", c), choice);
    }
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "Appearance" })).toBeVisible();
    if (expectLight) await expect(html(page)).toHaveAttribute("data-theme", "light");
    else await expect(html(page)).not.toHaveAttribute("data-theme", "light");
    await expect(page.locator("main").getByRole("radio", { name: choice === "system" ? "Auto" : choice === "light" ? "Light" : "Dark" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await page.waitForTimeout(200);
    await page.screenshot({ path: path.join(SHOTS, `theme-os-${os}-choice-${choice}-settings.png`), fullPage: true });
    await ctx.close();
  });
}

test("choosing Dark under an OS in light sticks across a reload", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "light" });
  const page = await ctx.newPage();
  await stubApi(page, { designPreview: ["theme_switch"] });
  await page.goto("/settings");
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  await page.locator("main").getByRole("radio", { name: "Dark" }).click();
  await expect(html(page)).not.toHaveAttribute("data-theme", "light");
  expect(await page.evaluate(() => localStorage.getItem("rivolt.theme"))).toBe("dark");
  await page.reload();
  // Before React: theme-init.js already honours the choice.
  await expect(html(page)).not.toHaveAttribute("data-theme", "light");
  await page.locator("main").getByRole("radio", { name: "Auto" }).click();
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  expect(await page.evaluate(() => localStorage.getItem("rivolt.theme"))).toBeNull();
  await ctx.close();
});

test("header switch: one tap sets the theme, stays in sync with Settings, fits at 390 px and aA 300 %", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "light" });
  const page = await ctx.newPage();
  await stubApi(page, { designPreview: ["theme_switch"] });
  await page.goto("/settings");
  const sw = page.locator("[data-theme-switch]");
  await expect(sw.getByRole("radio", { name: "Auto" })).toHaveAttribute("aria-checked", "true");
  for (const [name, light] of [["Dark", false], ["Light", true], ["Dark", false]] as const) {
    await sw.getByRole("radio", { name }).click();
    if (light) await expect(html(page)).toHaveAttribute("data-theme", "light");
    else await expect(html(page)).not.toHaveAttribute("data-theme", "light");
    await expect(page.locator("main").getByRole("radio", { name })).toHaveAttribute("aria-checked", "true");
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(SHOTS, `theme-header-${name.toLowerCase()}-390.png`) });
  }
  // One tab stop; the arrow keys move the choice.
  await sw.getByRole("radio", { name: "Dark" }).focus();
  await page.keyboard.press("ArrowRight");
  await expect(sw.getByRole("radio", { name: "Auto" })).toBeFocused();
  await expect(sw.getByRole("radio", { name: "Auto" })).toHaveAttribute("aria-checked", "true");
  // Every control inside the viewport, and a 44 px tap row.
  const box = await sw.boundingBox();
  const settings = await page.locator("[data-settings-button]").boundingBox();
  expect(box!.x + box!.width).toBeLessThanOrEqual(settings!.x + 1);
  expect(settings!.x + settings!.width).toBeLessThanOrEqual(390);
  await sw.getByRole("radio", { name: "Auto" }).click();
  await page.screenshot({ path: path.join(SHOTS, "theme-header-auto-390.png") });

  // Safari aA 300 %: about 130 CSS px wide. One button cycles Auto, Light, Dark.
  await page.setViewportSize({ width: 130, height: 700 });
  await expect(sw).toBeHidden();
  const cycle = page.locator("[data-theme-cycle]");
  await expect(cycle).toBeVisible();
  await expect(cycle).toHaveAttribute("aria-label", "Theme: Auto. Switch to Light");
  await cycle.click();
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  await expect(cycle).toHaveAttribute("aria-label", "Theme: Light. Switch to Dark");
  const cb = await cycle.boundingBox();
  const sb = await page.locator("[data-settings-button]").boundingBox();
  expect(cb!.x).toBeGreaterThanOrEqual(0);
  expect(cb!.x + cb!.width).toBeLessThanOrEqual(sb!.x + 1);
  expect(sb!.x + sb!.width).toBeLessThanOrEqual(130);
  await page.screenshot({ path: path.join(SHOTS, "theme-header-cycle-130.png") });
  await ctx.close();
});

test("theme_switch off (prod, older server, unknown keys): no header switch, Settings says System", async ({ browser }) => {
  for (const designPreview of [[], ["something_else"]]) {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "light" });
    const page = await ctx.newPage();
    await stubApi(page, { designPreview });
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "Appearance" })).toBeVisible();
    await expect(page.locator("main").getByRole("radio", { name: "System" })).toBeVisible();
    await expect(page.locator("main").getByRole("radio", { name: "Auto" })).toHaveCount(0);
    await expect(page.locator("[data-theme-switch]")).toHaveCount(0);
    await expect(page.locator("[data-theme-cycle]")).toHaveCount(0);
    await ctx.close();
  }
});
