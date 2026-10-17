import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Something links to /lease-calculator: the footer (app and landing) and the landing's lease claim.
for (const w of [390, 1280] as const) {
  test(`app footer links the calculator · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.goto("/settings");
    const link = page.locator("footer").getByRole("link", { name: "Lease calculator" });
    await expect(link).toHaveAttribute("href", "/lease-calculator");
    expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
    await link.click();
    await expect(page.getByRole("heading", { name: "Rivian lease mileage calculator", level: 1 })).toBeVisible();
    await ctx.close();
  });

  test(`landing links the calculator in the lease claim and the footer · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    await stubApi(page, { signedIn: false, landing: true });
    await page.goto("/");
    await expect(page.getByRole("link", { name: "Try the free lease calculator" })).toHaveAttribute("href", "/lease-calculator");
    await expect(page.locator("footer").getByRole("link", { name: "Lease calculator" })).toHaveAttribute("href", "/lease-calculator");
    expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
    await page.getByRole("link", { name: "Try the free lease calculator" }).click();
    await expect(page.getByRole("heading", { name: "Rivian lease mileage calculator", level: 1 })).toBeVisible();
    await ctx.close();
  });
}

test("the demo footer does not link it (the demo host has no such page)", async ({ page }) => {
  await stubApi(page, { signedIn: false, demo: true });
  await page.goto("/demo");
  await expect(page.locator("footer").getByRole("link", { name: "Privacy" })).toBeVisible();
  await expect(page.locator("footer").getByRole("link", { name: "Lease calculator" })).toHaveCount(0);
});
