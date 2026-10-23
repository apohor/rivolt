import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// A deploy while a tab is open: the next health check reports a new version, the
// bar appears (both looks, flag-independent), and Reload reloads the page.
for (const ui of ["on"] as const) {
  test(`new version bar and reload · ui ${ui}`, async ({ page }) => {
    await stubApi(page);
    let version = "v1.0.0";
    await page.route("**/api/health", (r) =>
      r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ok: true, version, time: new Date(e2eNow()).toISOString() }) }),
    );
    await page.goto("/");
    await expect(page.getByRole("region", { name: "Now" })).toBeVisible();
    await expect(page.getByTestId("version-bar")).toHaveCount(0);
    version = "v1.0.1";
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    const bar = page.getByTestId("version-bar");
    await expect(bar).toBeVisible();
    await expect(bar).toContainText("New version available");
    const navigated = page.waitForEvent("framenavigated");
    await bar.getByRole("button", { name: "Reload" }).click();
    await navigated;
  });
}

test("a failing health check shows nothing (offline shell keeps working)", async ({ page }) => {
  await stubApi(page);
  await page.route("**/api/health", (r) => r.abort());
  await page.goto("/");
  await expect(page.getByRole("region", { name: "Now" })).toBeVisible();
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await page.waitForTimeout(500);
  await expect(page.getByTestId("version-bar")).toHaveCount(0);
});
