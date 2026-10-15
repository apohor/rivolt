import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Signed-out demo: receipts and Activity rows open working, read-only
// detail views under /demo, and back / refresh / a fresh open all work.

async function openDemo(page: Page, path: string) {
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true });
  await page.goto(path);
}

test("a Money receipt opens a read-only charge detail; back and refresh work", async ({ page }) => {
  await openDemo(page, "/demo?tab=money");
  await expect(page.getByRole("region", { name: "Statement" })).toBeVisible();
  const link = page.locator('a[href^="/demo/charges/"]').first();
  await expect(link).toBeVisible();
  await link.click();
  await expect(page).toHaveURL(/\/demo\/charges\//);
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  // Read-only: no price editing, no delete.
  await expect(page.getByText("Delete this charge")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Mark as free" })).toHaveCount(0);
  await expect(page.getByText("Save pricing")).toHaveCount(0);
  // The sample banner is still there.
  await expect(page.getByText("Sample truck.")).toBeVisible();
  // Refresh on the detail route.
  await page.reload();
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  // Back lands on the same tab.
  await page.getByRole("link", { name: /Back to the demo/ }).click();
  await expect(page).toHaveURL(/\/demo$/);
});

test("Activity drive and charge cards open demo detail routes", async ({ page }) => {
  await openDemo(page, "/demo?tab=activity");
  const drive = page.locator('a[href^="/demo/drives/"]').first();
  await expect(drive).toBeVisible();
  await drive.click();
  await expect(page).toHaveURL(/\/demo\/drives\//);
  await expect(page.getByRole("heading", { name: /Drive/ }).first()).toBeVisible();
  await expect(page.getByText("Sample truck.")).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/\/demo\?tab=activity/);
  const charge = page.locator('a[href^="/demo/charges/"]').first();
  await expect(charge).toBeVisible();
  await charge.click();
  await expect(page).toHaveURL(/\/demo\/charges\//);
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
});

test("a fresh open of a demo detail URL works signed out", async ({ page }) => {
  await openDemo(page, "/demo");
  await page.getByRole("button", { name: "Activity" }).click();
  const href = await page.locator('a[href^="/demo/charges/"]').first().getAttribute("href");
  expect(href).toBeTruthy();
  await page.goto(href!);
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  // Never bounced to the login page.
  expect(new URL(page.url()).pathname).not.toBe("/login");
});

test("the demo banner CTA goes to signup, or to login when sign-ups are closed", async ({ page }) => {
  await openDemo(page, "/demo");
  await expect(page.getByRole("link", { name: "Connect my Rivian" })).toHaveAttribute("href", "/signup");
});

test("closed sign-ups send the demo CTA to login", async ({ page }) => {
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true, signupDisabled: true });
  await page.goto("/demo");
  await expect(page.getByRole("link", { name: "Connect my Rivian" })).toHaveAttribute("href", "/login");
});

test("the demo never requests the chargers pmtiles layer", async ({ page }) => {
  const tileReqs: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes(".pmtiles")) tileReqs.push(r.url());
  });
  await openDemo(page, "/demo?tab=activity");
  await page.locator('a[href^="/demo/drives/"]').first().click();
  await expect(page.getByRole("heading", { name: /Drive/ }).first()).toBeVisible();
  await page.waitForTimeout(1500);
  await page.goBack();
  await page.locator('a[href^="/demo/charges/"]').first().click();
  await expect(page.getByRole("heading", { name: /Charge/ }).first()).toBeVisible();
  await page.waitForTimeout(1500);
  expect(tileReqs).toEqual([]);
});
