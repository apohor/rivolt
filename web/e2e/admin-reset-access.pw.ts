import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// The admin user page's "Reset this account's access…": a typed confirmation, the temporary password shown once (and gone after
// Done), and a failed step shown with no password and a way to run it again.
const USER = {
  id: "u2", username: "quiet@example.com", email: "quiet@example.com", display_name: "Quiet", role: "user", disabled: false, vehicle_count: 1, drive_count: 4,
  import_count: 0, rivian_connected: true, needs_reauth: false, created_at: "2026-09-01T00:00:00Z", last_seen_at: "2026-10-24T00:00:00Z", last_data_at: "2026-10-24T00:00:00Z", leased: false, receipts_opt_in: false,
};
const DETAIL = { onboarding_completed: true, updated_at: "2026-10-24T00:00:00Z", active_sessions: 2, drive_miles_total: 10, charge_count: 1, charge_kwh_total: 5, sample_count: 10, vehicles: [] };

async function open(page: Page, secure: { status: number; body: unknown }[]) {
  const calls: string[] = [];
  await stubApi(page);
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: [USER] } }));
  await page.route("**/api/admin/users/u2", (r) => r.fulfill({ json: { ...USER, ...DETAIL } }));
  await page.route("**/api/admin/audit**", (r) => r.fulfill({ json: { items: [], next_before: null } }));
  await page.route("**/api/admin/users/u2/secure", (r) => {
    calls.push("secure");
    const a = secure[Math.min(calls.length - 1, secure.length - 1)];
    return r.fulfill({ status: a.status, contentType: "application/json", body: JSON.stringify(a.body) });
  });
  await page.goto("/admin/users/u2");
  return calls;
}

async function confirm(page: Page) {
  await page.getByRole("button", { name: "Reset this account's access…" }).click();
  const dialog = page.getByRole("alertdialog", { name: /Reset access for/ });
  await expect(dialog.getByRole("button", { name: "Reset access" })).toBeDisabled();
  await dialog.locator("input").fill("quiet@example.com");
  await dialog.getByRole("button", { name: "Reset access" }).click();
}

test("the new password is shown once, with the rule for handing it over, and is gone after Done", async ({ page }) => {
  const calls = await open(page, [{ status: 200, body: { ok: true, temporary_password: "Zq7-temp-pass", sessions_revoked: 2, passkeys_removed: 1 } }]);
  await confirm(page);
  const box = page.locator("[data-temp-password]");
  await expect(box.locator("[data-temp-password-value]")).toHaveText("Zq7-temp-pass");
  await expect(box).toContainText("ONLY to the email address on the account");
  await expect(box).toContainText("quiet@example.com");
  expect(calls).toEqual(["secure"]);
  await box.getByRole("button", { name: "Done, clear it" }).click();
  await expect(page.locator("[data-temp-password]")).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText("Zq7-temp-pass");
});

test("a step that fails shows which, no password, and Run again", async ({ page }) => {
  const calls = await open(page, [
    { status: 502, body: { error: "incomplete", step: "delete_passkeys" } },
    { status: 200, body: { ok: true, temporary_password: "Zq7-temp-pass", sessions_revoked: 0, passkeys_removed: 3 } },
  ]);
  await confirm(page);
  const stopped = page.locator("[data-reset-stopped]");
  await expect(stopped).toContainText("It stopped while removing the passkeys. No password was returned.");
  await expect(page.locator("[data-temp-password]")).toHaveCount(0);
  await stopped.getByRole("button", { name: "Run again" }).click();
  await expect(page.locator("[data-temp-password-value]")).toHaveText("Zq7-temp-pass");
  expect(calls).toEqual(["secure", "secure"]);
});
