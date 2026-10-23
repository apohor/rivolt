import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Money says "Fast" for fast charging (bar legend, filter chip, receipt tag), like Now and
// Activity; "DCFC" stays an internal value.
for (const ui of ["on"] as const) {
  test(`Money names fast charging "Fast", not "DCFC" · ui ${ui}`, async ({ page }) => {
    await stubApi(page, { scenario: "parked" });
    await page.goto("/money");
    await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
    const main = page.locator("main");
    await expect(main.getByRole("button", { name: "Fast", exact: true })).toBeVisible();
    await expect(main.getByText("Fast", { exact: true }).first()).toBeVisible();
    await expect(main.getByText("DCFC", { exact: true })).toHaveCount(0);
    await expect(main.getByRole("button", { name: "DCFC", exact: true })).toHaveCount(0);
    // The energy bar's label.
    await expect(main.getByRole("img", { name: /Fast/ }).first()).toBeVisible();
  });
}
