import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The iPhone app's "Open account deletion" opens /settings?tab=data&section=delete in Safari. A URL fragment is dropped by the sign-in
// redirect, a query is not: signing in from that link must end on the Danger zone.
const LINK = "/settings?tab=data&section=delete";

test("the deletion link lands on the Danger zone", async ({ page }) => {
  await stubApi(page);
  await page.goto(LINK);
  const card = page.locator("#danger");
  await expect(card).toBeVisible();
  await expect(card.getByText("Danger zone")).toBeVisible();
  await expect.poll(async () => card.evaluate((el) => el.getBoundingClientRect().top < window.innerHeight)).toBe(true);
  expect(new URL(page.url()).searchParams.get("section")).toBe("delete");
});

test("signing in from the deletion link returns to it, on the Danger zone", async ({ page }) => {
  await stubApi(page); // signed in: the sign-in page returns to ?next= at once
  await page.goto(`/login?next=${encodeURIComponent(LINK)}`);
  await expect(page).toHaveURL(/\/settings\?tab=data&section=delete/);
  await expect(page.locator("#danger")).toBeVisible();
  await expect.poll(async () => page.locator("#danger").evaluate((el) => el.getBoundingClientRect().top < window.innerHeight)).toBe(true);
});

test("the old #danger link still works", async ({ page }) => {
  await stubApi(page);
  await page.goto("/settings?tab=data#danger");
  await expect(page.locator("#danger")).toBeVisible();
});
