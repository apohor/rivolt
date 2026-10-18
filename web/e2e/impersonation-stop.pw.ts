import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Leaving "view as": the admin's exit tells the server (audit) and always gets out, even when that
// call fails.
const TARGET = { id: "11111111-2222-3333-4444-555555555555", email: "user@example.com" };

async function viewAs(page: import("@playwright/test").Page) {
  await stubApi(page);
  await page.goto("/");
  // Set after load (an init script would re-set it on the /admin page the Exit goes to).
  await page.evaluate((t) => {
    sessionStorage.setItem("rivolt.impersonate", JSON.stringify(t));
    window.dispatchEvent(new Event("rivolt:impersonation-change"));
  }, TARGET);
  await expect(page.getByText("Viewing as")).toBeVisible();
}

test("Exit posts the stop as the admin, then leaves view-as", async ({ page }) => {
  await page.route("**/api/admin/impersonation/stop", (r) => r.fulfill({ status: 204 }));
  await viewAs(page);
  const sent = page.waitForRequest("**/api/admin/impersonation/stop");
  await page.getByRole("button", { name: "Exit" }).click();
  const req = await sent;
  expect(req.method()).toBe("POST");
  expect(req.postDataJSON()).toEqual({ target_id: TARGET.id });
  expect(req.headers()["x-rivolt-impersonate"]).toBeUndefined();
  await expect(page).toHaveURL(/\/admin$/);
  expect(await page.evaluate(() => sessionStorage.getItem("rivolt.impersonate"))).toBeNull();
});

test("a failing stop call does not trap the admin in view-as", async ({ page }) => {
  await page.route("**/api/admin/impersonation/stop", (r) => r.abort("failed"));
  await viewAs(page);
  await page.getByRole("button", { name: "Exit" }).click();
  await expect(page).toHaveURL(/\/admin$/);
  expect(await page.evaluate(() => sessionStorage.getItem("rivolt.impersonate"))).toBeNull();
  await expect(page.getByText("Viewing as")).toHaveCount(0);
});
