import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// /support: the App Store's Support URL. Public (signed out), with the support email, and linked from the footer beside
// Privacy and Terms.
test("support renders signed out, shows the support email and is in the footer", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.goto("/support");
  const doc = page.locator("[data-legal]");
  await expect(doc.getByRole("heading", { name: "Rivolt support", level: 1 })).toBeVisible();
  await expect(doc.getByRole("link", { name: "support@rivolt.dev" })).toHaveAttribute("href", "mailto:support@rivolt.dev");
  await expect(doc).toContainText("not affiliated with, endorsed by or sponsored by Rivian");
  const footer = page.locator("footer");
  await expect(footer.getByRole("link", { name: "Privacy" })).toBeVisible();
  await expect(footer.getByRole("link", { name: "Terms" })).toBeVisible();
  await footer.getByRole("link", { name: "Support" }).click();
  await expect(page).toHaveURL(/\/support$/);
});
