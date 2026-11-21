import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// signup_v2 copy: landing doors, sign-up order and sample-truck link, with the key on and off, at 320, 393 and 1440, light and dark.
const noOverflow = (page: import("@playwright/test").Page) =>
  page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);

for (const width of [320, 393, 1440]) {
  for (const scheme of ["light", "dark"] as const) {
    test(`landing, key on · ${width} · ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width, height: 900 }, colorScheme: scheme });
      const page = await ctx.newPage();
      await stubApi(page, { signedIn: false, landing: true, demo: true, designPreview: ["signup_v2"] });
      await page.goto("/");
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      const add = page.locator(".lp-hero").getByRole("link", { name: "Add my truck" });
      const sample = page.locator(".lp-hero").getByRole("link", { name: "Try a sample truck" });
      await expect(add).toBeVisible();
      await expect(sample).toBeVisible();
      await expect(add).toHaveAttribute("href", "/signup");
      await expect(page.getByRole("heading", { name: "Free tier and Pro tier" })).toBeAttached();
      expect(await noOverflow(page)).toBe(true);
      await ctx.close();
    });
  }
}

test("landing, key off: today's labels", async ({ page }) => {
  await stubApi(page, { signedIn: false, landing: true, demo: true, designPreview: [] });
  await page.goto("/");
  await expect(page.locator(".lp-hero").getByRole("link", { name: "Connect your Rivian" })).toBeVisible();
  await expect(page.locator(".lp-hero").getByRole("link", { name: "Plan a sample trip" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Free and Pro" })).toBeAttached();
});

for (const width of [320, 393, 1440]) {
  test(`sign-up, key on · ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await stubApi(page, { signedIn: false, demo: true, designPreview: ["signup_v2"] });
    await page.goto("/signup");
    await expect(page.getByRole("heading", { name: "Create your Rivolt account" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Try a sample truck" })).toHaveAttribute("href", "/demo");
    expect(await noOverflow(page)).toBe(true);
  });
}

test("sign-up, key off: today's screen", async ({ page }) => {
  await stubApi(page, { signedIn: false, demo: true, designPreview: [] });
  await page.goto("/signup");
  await expect(page.getByRole("heading", { name: "Create your account" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Try a sample truck" })).toHaveCount(0);
});
