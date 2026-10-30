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

test("signed out, the 401 bounce keeps the link, and signing in returns to the Danger zone", async ({ page }) => {
  // The real path: the Settings page's own request is refused (401), the API client sends the browser to /login?next=…, and once the
  // person has signed in the sign-in page returns to ?next=.
  let signedIn = false;
  await stubApi(page, { signedIn: false });
  await page.route("**/api/auth/me", (r) =>
    signedIn ? r.fulfill({ json: { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true } }) : r.fulfill({ status: 401, json: { error: "unauthenticated" } }),
  );
  await page.route("**/api/settings/**", (r) => (signedIn ? r.fallback() : r.fulfill({ status: 401, json: { error: "unauthenticated" } })));
  await page.goto(LINK);
  await expect(page).toHaveURL(/\/login\?next=/);
  expect(new URL(page.url()).searchParams.get("next")).toBe(LINK);
  signedIn = true;
  await page.reload(); // the sign-in finished; the sign-in page returns to ?next=
  await expect(page).toHaveURL(/\/settings\?tab=data&section=delete/);
  await expect(page.locator("#danger")).toBeVisible();
  await expect.poll(async () => page.locator("#danger").evaluate((el) => el.getBoundingClientRect().top < window.innerHeight)).toBe(true);
});

test("the sign-in page returns to ?next= with the section intact", async ({ page }) => {
  await stubApi(page); // already signed in
  await page.goto(`/login?next=${encodeURIComponent(LINK)}`);
  await expect(page).toHaveURL(/\/settings\?tab=data&section=delete/);
  await expect(page.locator("#danger")).toBeVisible();
});

test("the old #danger link still works", async ({ page }) => {
  await stubApi(page);
  await page.goto("/settings?tab=data#danger");
  await expect(page.locator("#danger")).toBeVisible();
});
