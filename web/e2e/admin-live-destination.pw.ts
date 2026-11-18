import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// Admin > Operations > Live destination. Named users or Everyone; each change has a confirm.
const H = 3_600_000;
const ago = (ms: number) => new Date(e2eNow() - ms).toISOString();
const mk = (id: string, name: string, role = "user") => ({ id, username: `${name.toLowerCase()}@example.com`, email: `${name.toLowerCase()}@example.com`, display_name: name, role, disabled: false, created_at: ago(900 * H), vehicle_count: 1, drive_count: 1, import_count: 0, rivian_connected: true, needs_reauth: false });
const U1 = "11111111-1111-1111-1111-111111111111", U2 = "22222222-2222-2222-2222-222222222222", U3 = "33333333-3333-3333-3333-333333333333";
const USERS = [mk(U1, "Owner", "admin"), mk(U2, "Ann"), mk(U3, "Bob")];

async function open(page: Page, users: string[]) {
  let state: { all?: boolean; users: string[] } = { users };
  const calls: string[] = [];
  await stubApi(page);
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: USERS } }));
  await page.route("**/api/admin/audit**", (r) => r.fulfill({ json: { items: [], next_before: null } }));
  await page.route("**/api/admin/kill-switch", (r) =>
    r.fulfill({ json: { kill_switch: { paused: false }, trip_planner: { enabled: true }, live_destination: state } }),
  );
  await page.route("**/api/admin/flags/live-destination", (r) => {
    const b = r.request().postDataJSON();
    calls.push(JSON.stringify(b));
    state = b.all ? { all: true, users: [] } : { users: b.users };
    return r.fulfill({ json: { live_destination: state } });
  });
  await page.goto("/admin#operations");
  await expect(page.locator("[data-admin-flags]")).toBeVisible();
  return calls;
}
const row = (page: Page) => page.locator("[data-flag=live_destination]");

test("lists the users, adds one and removes one", async ({ page }) => {
  const calls = await open(page, [U2]);
  const r = row(page);
  await expect(r.locator("[data-flag-value]")).toHaveText("1 user");
  await r.getByRole("button", { name: "Change…" }).click();
  await r.getByLabel("Selected users").check();
  await expect(r.getByLabel(/Ann/)).toBeChecked();
  await r.getByLabel(/Bob/).check();
  await r.getByRole("button", { name: "Save…" }).click();
  await r.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls).toEqual([JSON.stringify({ all: false, users: [U2, U3] })]);
  await expect(r.locator("[data-flag-value]")).toHaveText("2 users");

  await r.getByRole("button", { name: "Change…" }).click();
  await r.getByLabel(/Ann/).uncheck();
  await r.getByRole("button", { name: "Save…" }).click();
  await r.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls).toEqual([JSON.stringify({ all: false, users: [U2, U3] }), JSON.stringify({ all: false, users: [U3] })]);
  await expect(r.locator("[data-flag-value]")).toHaveText("1 user");
});

test("Nobody clears the list and Cancel sends nothing", async ({ page }) => {
  const calls = await open(page, [U2, U3]);
  const r = row(page);
  await r.getByRole("button", { name: "Change…" }).click();
  await r.getByRole("button", { name: "Cancel" }).click();
  expect(calls).toEqual([]);
  await r.getByRole("button", { name: "Change…" }).click();
  await r.getByLabel("Nobody").check();
  await r.getByRole("button", { name: "Save…" }).click();
  await r.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls).toEqual([JSON.stringify({ all: false, users: [] })]);
  await expect(r.locator("[data-flag-value]")).toHaveText("Nobody");
});

test("Everyone asks first and sends all", async ({ page }) => {
  const calls = await open(page, [U2]);
  const r = row(page);
  await r.getByRole("button", { name: "Change…" }).click();
  await r.getByLabel("Everyone").check();
  await r.getByRole("button", { name: "Save…" }).click();
  await expect(r.getByText("Turn the live destination on for everyone?")).toBeVisible();
  expect(calls).toEqual([]);
  await r.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls).toEqual([JSON.stringify({ all: true, users: [] })]);
  await expect(r.locator("[data-flag-value]")).toHaveText("Everyone");
});
