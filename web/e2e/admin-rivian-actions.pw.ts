import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// The admin-only site never talks to Rivian: with /api/config features.rivian_actions === false the Users
// table and the user detail drop Sync and Refresh session and say where those run. Absent or true
// (an older server, or rivolt.dev itself) keeps them.
const H = 3_600_000;
const ago = (ms: number) => new Date(e2eNow() - ms).toISOString();
const base = { disabled: false, vehicle_count: 1, drive_count: 1, import_count: 0, rivian_connected: true };
const USERS = [
  { ...base, id: "a1", username: "owner@example.com", email: "owner@example.com", display_name: "Owner", role: "admin", needs_reauth: false, created_at: ago(900 * H) },
  { ...base, id: "u1", username: "ann@example.com", email: "ann@example.com", display_name: "Ann", role: "user", needs_reauth: true, needs_reauth_at: ago(H), created_at: ago(40 * H * 24) },
];

async function open(page: Page, rivianActions: boolean | "absent") {
  await stubApi(page);
  await page.route("**/api/config", (r) =>
    r.fulfill({ json: { features: { trip_planner_enabled: true, signup_enabled: true, ...(rivianActions === "absent" ? {} : { rivian_actions: rivianActions }) }, tiles: { url: "", chargers_url: "" }, valhalla: { path: "" }, impersonation: { disabled: true } } }),
  );
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: USERS } }));
  await page.route("**/api/admin/users/*", (r) =>
    r.fulfill({ json: { ...USERS[1], onboarding_completed: true, updated_at: ago(H), active_sessions: 0, needs_reauth_reason: "rejected", drive_miles_total: 0, charge_count: 0, charge_kwh_total: 0, sample_count: 0, vehicles: [] } }),
  );
  await page.goto("/admin/users/u1");
}

for (const w of [390, 1280] as const) {
  test(`rivian_actions=false hides Sync and Refresh in the list and on the user page · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    await open(page, false);
    // The user's page: the note, no Rivian actions, the rest still there.
    await expect(page.locator("[data-rivian-actions-note]")).toHaveText("Rivian actions run on rivolt.dev only");
    await expect(page.getByRole("button", { name: "Sync Rivian" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Refresh session" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /Disable account/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Delete user/ })).toBeVisible();
    // The list's row actions.
    await page.goto("/admin/users");
    const row = page.locator("[data-user-row=u1]");
    await expect(row.getByRole("button", { name: "Disable Ann" })).toBeVisible();
    await expect(row.getByRole("button", { name: /^Sync/ })).toHaveCount(0);
    await expect(row.getByRole("button", { name: /^Refresh/ })).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
    await ctx.close();
  });
}

for (const v of [true, "absent"] as const) {
  test(`rivian_actions ${v === "absent" ? "absent (older server)" : "true"} keeps them`, async ({ page }) => {
    await open(page, v);
    await expect(page.getByRole("button", { name: "Sync Rivian" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Refresh session" })).toBeVisible();
    await expect(page.locator("[data-rivian-actions-note]")).toHaveCount(0);
    await page.goto("/admin/users");
    const row = page.locator("[data-user-row=u1]");
    await expect(row.getByRole("button", { name: "Sync Ann" })).toBeVisible();
    await expect(row.getByRole("button", { name: "Refresh Rivian session for Ann" })).toBeVisible();
  });
}
