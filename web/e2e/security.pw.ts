import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Settings → Security: change the password, or reset everything (a new password, every passkey removed, every other device signed
// out). Both ask for the current password and show whether or not passkeys are on.
async function open(page: Page, answers: { password?: { status: number; body: unknown }; secure?: { status: number; body: unknown } } = {}) {
  const sent: { password?: unknown; secure?: unknown } = {};
  await stubApi(page);
  await page.route("**/api/auth/password", (r) => {
    sent.password = r.request().postDataJSON();
    const a = answers.password ?? { status: 200, body: { ok: true, sessions_revoked: 2, passkeys: [] } };
    return r.fulfill({ status: a.status, contentType: "application/json", body: JSON.stringify(a.body) });
  });
  await page.route("**/api/auth/secure", (r) => {
    sent.secure = r.request().postDataJSON();
    const a = answers.secure ?? { status: 200, body: { ok: true, passkeys_removed: 3, sessions_revoked: 2 } };
    return r.fulfill({ status: a.status, contentType: "application/json", body: JSON.stringify(a.body) });
  });
  return sent;
}

test("changing the password asks for the current one and says what happened", async ({ page }) => {
  const sent = await open(page);
  await page.goto("/settings?tab=security");
  const form = page.locator("[data-security-form=password]");
  await expect(form.getByRole("button", { name: "Change password" })).toBeDisabled();
  await form.getByLabel("Current password").fill("old-pass-1");
  await form.getByLabel("New password", { exact: true }).fill("new-pass-2");
  await form.getByLabel("New password again").fill("different");
  await expect(form.getByText("The two new passwords differ.")).toBeVisible();
  await form.getByLabel("New password again").fill("new-pass-2");
  await form.getByRole("button", { name: "Change password" }).click();
  await expect(form.locator("[data-security-note]")).toContainText("Password changed. 2 other devices were signed out.");
  expect(sent.password).toEqual({ current: "old-pass-1", new: "new-pass-2" });
  await expect(form.getByLabel("Current password")).toHaveValue("");
});

test("the full reset needs a second press and says it removes the person's own passkeys too", async ({ page }) => {
  const sent = await open(page);
  await page.goto("/settings?tab=security");
  const form = page.locator("[data-security-form=reset]");
  await form.getByLabel("Current password").fill("old-pass-1");
  await form.getByLabel("New password", { exact: true }).fill("new-pass-2");
  await form.getByLabel("New password again").fill("new-pass-2");
  await form.getByRole("button", { name: "Reset password and remove all passkeys" }).click();
  await expect(form.getByRole("alert")).toContainText("including your own");
  expect(sent.secure).toBeUndefined();
  await form.getByRole("button", { name: "Yes, reset everything" }).click();
  await expect(form.locator("[data-security-note]")).toContainText("3 passkeys removed and 2 other devices signed out");
  expect(sent.secure).toEqual({ current: "old-pass-1", new: "new-pass-2" });
});

test("a wrong password, an unfinished sign-out and a provider account each get the right words", async ({ page }) => {
  const sent = await open(page, { password: { status: 403, body: { error: "password_incorrect" } } });
  await page.goto("/settings?tab=security");
  const form = page.locator("[data-security-form=password]");
  const fill = async () => {
    await form.getByLabel("Current password").fill("x");
    await form.getByLabel("New password", { exact: true }).fill("new-pass-2");
    await form.getByLabel("New password again").fill("new-pass-2");
  };
  await fill();
  await form.getByRole("button", { name: "Change password" }).click();
  await expect(form.locator("[data-security-note]")).toHaveText("That isn't your current password.");
  expect(sent.password).toBeTruthy();
  await page.unroute("**/api/auth/password");
  await page.route("**/api/auth/password", (r) => r.fulfill({ status: 502, contentType: "application/json", body: JSON.stringify({ error: "password_changed_but_sign_out_incomplete", password_changed: true }) }));
  await fill();
  await form.getByRole("button", { name: "Change password" }).click();
  await expect(form.locator("[data-security-note]")).toContainText("Your password was changed, but signing out your other devices didn't finish");
  await page.unroute("**/api/auth/password");
  await page.route("**/api/auth/password", (r) => r.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: "needs_an_email_account" }) }));
  await fill();
  await form.getByRole("button", { name: "Change password" }).click();
  await expect(page.locator("[data-security-form]")).toHaveCount(0);
});
