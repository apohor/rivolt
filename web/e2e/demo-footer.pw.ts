import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The demo footer matches the signed-in one: GitHub, MIT, Docs, Discord, version.
test("demo footer has the Discord link and the version", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo");
  const footer = page.locator("footer");
  await expect(footer.getByRole("link", { name: "Discord" })).toHaveAttribute("href", "https://discord.gg/FGty2xmdeV");
  await expect(footer.getByRole("link", { name: "GitHub" })).toBeVisible();
  await expect(footer.getByRole("link", { name: "vtest" })).toBeVisible();
});
