import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// /support: the App Store's Support URL. Public (signed out) with the support email, and linked beside Privacy and Terms
// wherever those are: the landing page's footer line (signed out) and the app's footer (signed in).
test("support renders signed out and shows the support email", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.goto("/support");
  const doc = page.locator("[data-legal]");
  await expect(doc.getByRole("heading", { name: "Support", level: 1 })).toBeVisible();
  await expect(doc.getByRole("link", { name: "support@rivolt.dev" })).toHaveAttribute("href", "mailto:support@rivolt.dev");
  await expect(doc).toContainText("not affiliated with Rivian");
});

test("the landing page's footer line links it beside Privacy and Terms", async ({ page }) => {
  await stubApi(page, { signedIn: false, landing: true });
  await page.goto("/");
  await expect(page.getByRole("link", { name: "Privacy", exact: true }).first()).toBeVisible();
  await expect(page.getByRole("link", { name: "Terms", exact: true }).first()).toBeVisible();
  await page.getByRole("link", { name: "Support", exact: true }).click();
  await expect(page).toHaveURL(/\/support$/);
  await expect(page.locator("[data-legal]")).toContainText("support@rivolt.dev");
});

test("the app's footer links it beside Privacy and Terms", async ({ page }) => {
  await stubApi(page);
  await page.goto("/");
  const footer = page.locator("footer");
  await expect(footer.getByRole("link", { name: "Privacy" })).toBeVisible();
  await expect(footer.getByRole("link", { name: "Terms" })).toBeVisible();
  await footer.getByRole("link", { name: "Support" }).click();
  await expect(page).toHaveURL(/\/support$/);
});
