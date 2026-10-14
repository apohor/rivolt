import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Theme option (System / Light / Dark): the choice beats the OS, is kept
// per device, and survives a reload without a flash (theme-init.js).

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
    await stubApi(page);
    if (choice !== "system") {
      await page.addInitScript((c) => localStorage.setItem("rivolt.theme", c), choice);
    }
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "Appearance" })).toBeVisible();
    if (expectLight) await expect(html(page)).toHaveAttribute("data-theme", "light");
    else await expect(html(page)).not.toHaveAttribute("data-theme", "light");
    await expect(page.getByRole("radio", { name: choice === "system" ? "System" : choice === "light" ? "Light" : "Dark" })).toHaveAttribute(
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
  await stubApi(page);
  await page.goto("/settings");
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  await page.getByRole("radio", { name: "Dark" }).click();
  await expect(html(page)).not.toHaveAttribute("data-theme", "light");
  expect(await page.evaluate(() => localStorage.getItem("rivolt.theme"))).toBe("dark");
  await page.reload();
  // Before React: theme-init.js already honours the choice.
  await expect(html(page)).not.toHaveAttribute("data-theme", "light");
  await page.getByRole("radio", { name: "System" }).click();
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  expect(await page.evaluate(() => localStorage.getItem("rivolt.theme"))).toBeNull();
  await ctx.close();
});
