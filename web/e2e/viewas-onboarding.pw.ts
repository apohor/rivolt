import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// "View as" a user who has not finished onboarding (or accepted the Terms): the admin must not be
// sent to /onboarding or the gate, which need writes a read-only view refuses, and must always
// have the banner with Exit.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
const TARGET = { id: "11111111-2222-3333-4444-555555555555", email: "demo@example.com" };

async function open(page: Page, o: { terms?: boolean } = {}) {
  await stubApi(page, { rivian: "disconnected" });
  await page.addInitScript((t) => sessionStorage.setItem("rivolt.impersonate", JSON.stringify(t)), TARGET);
  await page.route("**/api/auth/me", (r) =>
    r.fulfill({
      json: {
        user_id: TARGET.id, username: TARGET.email, role: "user", onboarding_completed: false,
        ...(o.terms ? { terms: { current: "2026-10-17", accepted: null, accepted_at: null, required: true } } : {}),
      },
    }),
  );
  const writes: string[] = [];
  await page.route("**/api/onboarding/complete", (r) => {
    writes.push("complete");
    return r.fulfill({ status: 405, json: { error: "impersonated sessions are read-only" } });
  });
  return writes;
}

for (const w of [390, 1280] as const) {
  test(`no onboarding redirect while viewing as, banner on every route · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    await open(page);
    await page.goto("/");
    await expect(page.getByText("Viewing as")).toBeVisible();
    await page.waitForTimeout(600);
    await expect(page).toHaveURL(/\/$/); // not sent to /onboarding
    // Landing on /onboarding directly (a bookmark, an old redirect) still has the banner and Exit.
    await page.goto("/onboarding");
    await expect(page.getByText("Viewing as")).toBeVisible();
    await expect(page.getByRole("button", { name: "Exit" })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS, `viewas-onboarding-${w}.png`) });
    await page.getByRole("button", { name: "Exit" }).click();
    await expect(page).toHaveURL(/\/admin$/);
    await ctx.close();
  });
}

test("a viewed user who has not accepted the Terms does not trap the admin behind the gate", async ({ page }) => {
  await open(page, { terms: true });
  await page.goto("/");
  await expect(page.getByText("Viewing as")).toBeVisible();
  await page.waitForTimeout(400);
  await expect(page.locator("[data-terms-gate]")).toHaveCount(0);
});

test("without view-as, an unfinished user is still sent to onboarding", async ({ page }) => {
  await open(page);
  await page.addInitScript(() => sessionStorage.removeItem("rivolt.impersonate"));
  await page.goto("/");
  await expect(page).toHaveURL(/\/onboarding$/);
});
