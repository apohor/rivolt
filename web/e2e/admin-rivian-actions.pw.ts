import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// The admin-only site never talks to Rivian: with /api/config.rivian_actions === false the Users
// table and the user detail drop Sync and Refresh session and say where those run. Absent or true
// (an older server, or rivolt.dev itself) keeps them.
const H = 3_600_000;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const base = { disabled: false, vehicle_count: 1, drive_count: 1, import_count: 0, rivian_connected: true };
const USERS = [
  { ...base, id: "a1", username: "owner@example.com", email: "owner@example.com", display_name: "Owner", role: "admin", needs_reauth: false, created_at: ago(900 * H) },
  { ...base, id: "u1", username: "ann@example.com", email: "ann@example.com", display_name: "Ann", role: "user", needs_reauth: true, needs_reauth_at: ago(H), created_at: ago(40 * H * 24) },
];

async function open(page: Page, rivianActions: boolean | "absent", nextUi: boolean) {
  await stubApi(page, { uiNext: nextUi });
  await page.route("**/api/config", (r) =>
    r.fulfill({ json: { features: { trip_planner_enabled: true, signup_enabled: true }, tiles: { url: "", chargers_url: "" }, valhalla: { path: "" }, impersonation: { disabled: true }, ...(rivianActions === "absent" ? {} : { rivian_actions: rivianActions }) } }),
  );
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: USERS } }));
  await page.route("**/api/admin/users/*", (r) =>
    r.fulfill({ json: { ...USERS[1], onboarding_completed: true, updated_at: ago(H), active_sessions: 0, needs_reauth_reason: "rejected", drive_miles_total: 0, charge_count: 0, charge_kwh_total: 0, sample_count: 0, vehicles: [] } }),
  );
  await page.goto("/admin#users");
}

for (const w of [390, 1280] as const) {
  test(`rivian_actions=false hides Sync and Refresh, new look · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    await open(page, false, true);
    await page.getByRole("button", { name: "Select Ann" }).click();
    await expect(page.locator("[data-rivian-actions-note]")).toHaveText("Rivian actions run on rivolt.dev only");
    await expect(page.getByRole("button", { name: /^Sync/ })).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^Refresh/ })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Sync Rivian" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Refresh session" })).toHaveCount(0);
    await expect(page.getByText("Try Sync Rivian below")).toHaveCount(0);
    // The rest of the row's actions are still there.
    await expect(page.locator("[data-user-row=u1]").getByRole("button", { name: "Disable Ann" })).toBeVisible();
    await expect(page.locator("[data-user-row=u1]").getByRole("button", { name: "Delete Ann" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
    await ctx.close();
  });
}

test("rivian_actions=false hides them in the flag-off admin too", async ({ page }) => {
  await open(page, false, false);
  await page.getByRole("button", { name: "Select Ann" }).click();
  await expect(page.locator("[data-rivian-actions-note]")).toBeVisible();
  await expect(page.getByRole("button", { name: "Sync Rivian" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Refresh session" })).toHaveCount(0);
});

for (const v of [true, "absent"] as const) {
  test(`rivian_actions ${v === "absent" ? "absent (older server)" : "true"} keeps them`, async ({ page }) => {
    await open(page, v, true);
    await expect(page.locator("[data-user-row=u1]").getByRole("button", { name: "Sync Ann" })).toBeVisible();
    await expect(page.locator("[data-user-row=u1]").getByRole("button", { name: "Refresh Rivian session for Ann" })).toBeVisible();
    await page.getByRole("button", { name: "Select Ann" }).click();
    await expect(page.getByRole("button", { name: "Sync Rivian" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Refresh session" })).toBeVisible();
    await expect(page.locator("[data-rivian-actions-note]")).toHaveCount(0);
  });
}
