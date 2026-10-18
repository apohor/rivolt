import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// /admin/users (the list) and /admin/users/:id (one user, as a page): deep links, a working back
// button, the sections, and the actions with their confirms.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const H = 3_600_000;
const D = 24 * H;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const base = { disabled: false, vehicle_count: 1, drive_count: 4, import_count: 0, rivian_connected: true, needs_reauth: false };
const USERS = [
  { ...base, id: "a1", username: "owner@example.com", email: "owner@example.com", display_name: "Owner", role: "admin", created_at: ago(300 * D), last_seen_at: ago(60_000), last_data_at: ago(5 * 60_000), leased: true, receipts_opt_in: true },
  { ...base, id: "u2", username: "quiet@example.com", email: "quiet@example.com", display_name: "Quiet", role: "user", created_at: ago(30 * D), last_seen_at: ago(2 * D), last_data_at: ago(5 * H), leased: true, receipts_opt_in: false },
  { ...base, id: "u3", username: "reauth@example.com", email: "reauth@example.com", display_name: "Reauth", role: "user", needs_reauth: true, needs_reauth_at: ago(26 * H), created_at: ago(70 * D), last_seen_at: ago(9 * D), last_data_at: ago(26 * H), leased: false, receipts_opt_in: false },
  { ...base, id: "u4", username: "idle@example.com", email: "idle@example.com", display_name: "Idle", role: "user", vehicle_count: 0, rivian_connected: false, created_at: ago(105 * D), last_seen_at: null, last_data_at: null, leased: false, receipts_opt_in: false, purge_warned_at: ago(10 * D) },
];
const DETAIL = {
  onboarding_completed: true, updated_at: ago(H), rivian_session_at: ago(3 * H), active_sessions: 2, drive_miles_total: 812, charge_count: 11, charge_kwh_total: 402,
  sample_count: 12345, oldest_sample_at: ago(60 * D), newest_sample_at: ago(5 * H), needs_reauth_reason: "Rivian rejected the stored session",
  vehicles: [{ id: "v1", rivian_vehicle_id: "01-1", vin: "7FCTGAAA1PN000001", display_name: "Quiet's R1S", model: "R1S", model_year: 2024, pack_kwh: 135, drive_count: 4, charge_count: 2, last_sample_at: ago(5 * H) }],
  signup_request: { requested_at: ago(31 * D), decided_at: ago(30 * D), status: "approved", message: "I have an R1S" },
};

async function open(page: Page, url: string, uiNext = true) {
  const calls: string[] = [];
  await stubApi(page, { uiNext });
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: USERS } }));
  await page.route("**/api/admin/users/*", (r) => {
    const id = new URL(r.request().url()).pathname.split("/").pop();
    if (r.request().method() === "DELETE") { calls.push(`delete ${id}`); return r.fulfill({ json: { ok: true, idp: "ok" } }); }
    const u = USERS.find((x) => x.id === id);
    return u ? r.fulfill({ json: { ...u, ...DETAIL } }) : r.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.route("**/api/admin/users/*/role", (r) => { calls.push(`role ${r.request().postData()}`); return r.fulfill({ json: { ok: true } }); });
  await page.route("**/api/admin/users/*/disabled", (r) => { calls.push(`disabled ${r.request().postData()}`); return r.fulfill({ json: { ok: true } }); });
  await page.route("**/api/admin/audit**", (r) => r.fulfill({ json: { items: [{ id: 1, at: ago(2 * H), actor_user_id: "a1", action: "user.sync_rivian", target_type: "user", target_id: "u2", target_user_id: "u2", details: {}, ip: "1.2.3.4", outcome: "ok", status: 200 }], next_before: null } }));
  await page.goto(url);
  return calls;
}
const onPage = (page: Page, id: string) => page.locator(`[data-user-page=${id}]`);

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`user page · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page, "/admin/users/u2");
      const pg = onPage(page, "u2");
      await expect(pg.getByRole("heading", { name: "Quiet", level: 1 })).toBeVisible();
      await expect(pg).toContainText("quiet@example.com");
      await expect(pg.getByText("User ID")).toBeVisible();
      await expect(pg).toContainText("Connected · session 3h ago");
      await expect(pg.locator("[data-no-data-alert]")).toHaveText("Leased, no data 2h+");
      await expect(pg).toContainText("5h ago");
      await expect(pg).toContainText("Vehicles (1)");
      await expect(pg).toContainText("Quiet's R1S");
      await expect(pg).toContainText("812 mi");
      await expect(pg.locator("[data-purge]")).toContainText("Not in scope");
      await expect(pg).toContainText("Signup origin");
      await expect(pg.locator("[data-user-audit]")).toContainText("Sync Rivian");
      await expect(page.getByRole("button", { name: "Users", exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `admin-user-page-${w}-${theme}.png`), fullPage: true });
      await ctx.close();
    });
  }
}

test("1280: the list opens a user from the name link or anywhere on the row, and Back returns to the list", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  await open(page, "/admin/users");
  await expect(page.locator("[data-admin-users]")).toBeVisible();
  await page.getByRole("link", { name: "Open Reauth" }).click();
  await expect(page).toHaveURL(/\/admin\/users\/u3$/);
  await expect(onPage(page, "u3")).toContainText("Needs re-auth, since 1d ago");
  await expect(onPage(page, "u3")).toContainText("Rivian rejected the stored session");
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/users$/);
  await page.locator("[data-user-row=u2]").locator("[data-col=rivian]").click(); // a plain cell
  await expect(page).toHaveURL(/\/admin\/users\/u2$/);
  await expect(onPage(page, "u2")).toBeVisible();
  await page.getByRole("link", { name: "← All users" }).click();
  await expect(page).toHaveURL(/\/admin\/users$/);
  await ctx.close();
});

test("390: tapping a card opens the user page; Back returns", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 800 }, hasTouch: true });
  const page = await ctx.newPage();
  await open(page, "/admin/users");
  await expect(page.locator("[data-users-head]")).toBeHidden(); // stacked rows on a phone
  await page.locator("[data-user-row=u4]").locator("[data-col=rivian]").tap();
  await expect(page).toHaveURL(/\/admin\/users\/u4$/);
  await expect(onPage(page, "u4").locator("[data-purge]")).toContainText("Purged in 15 days");
  await expect(onPage(page, "u4").locator("[data-purge]")).toContainText("A warning email was sent.");
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/users$/);
  await ctx.close();
});

test("an unknown user says so, with a way back", async ({ page }) => {
  await open(page, "/admin/users/nope");
  await expect(page.getByText("User not found")).toBeVisible();
  await page.getByRole("link", { name: "← All users" }).click();
  await expect(page).toHaveURL(/\/admin\/users$/);
});

test("the tabs: from a user page to another tab and back to Users", async ({ page }) => {
  await open(page, "/admin/users/u2");
  await page.getByRole("button", { name: "Signups", exact: true }).click();
  await expect(page).toHaveURL(/\/admin#signups$/);
  await page.getByRole("button", { name: "Users", exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/users$/);
  await expect(page.locator("[data-admin-users]")).toBeVisible();
});

test("actions ask first: role, disable, then delete needs the typed email and returns to the list", async ({ page }) => {
  const calls = await open(page, "/admin/users/u2");
  const acts = page.locator("[data-user-actions]");
  await acts.getByRole("button", { name: /Promote to admin/ }).click();
  await expect(page.getByRole("alertdialog", { name: "Confirm" })).toContainText("Make Quiet an admin?");
  await page.getByRole("button", { name: "Back" }).click();
  expect(calls).toEqual([]);
  await acts.getByRole("button", { name: /Promote to admin/ }).click();
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls).toEqual([`role {"role":"admin"}`]);
  await expect(page.getByRole("status")).toContainText("quiet@example.com is now an admin.");
  await acts.getByRole("button", { name: /Disable account/ }).click();
  await expect(page.getByRole("alertdialog", { name: "Confirm" })).toContainText("Block Quiet from signing in?");
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect.poll(() => calls.some((c) => c.startsWith("disabled"))).toBe(true);
  // Delete: the exact email, then back to the list.
  await acts.getByRole("button", { name: /Delete user/ }).click();
  const dlg = page.locator("[data-typed-confirm]");
  await expect(dlg.getByRole("button", { name: "Delete user" })).toBeDisabled();
  await dlg.getByLabel(/Type/).fill("quiet@example");
  await expect(dlg.getByRole("button", { name: "Delete user" })).toBeDisabled();
  await dlg.getByLabel(/Type/).fill("quiet@example.com");
  await dlg.getByRole("button", { name: "Delete user" }).click();
  await expect.poll(() => calls).toContain("delete u2");
  await expect(page).toHaveURL(/\/admin\/users$/);
});

test("the last admin can be neither demoted, disabled nor deleted", async ({ page }) => {
  await open(page, "/admin/users/a1");
  const acts = page.locator("[data-user-actions]");
  await expect(acts.getByRole("button", { name: /Demote to user/ })).toBeDisabled();
  await expect(acts.getByRole("button", { name: /Disable account/ })).toBeDisabled();
  await expect(acts.getByRole("button", { name: /Delete user/ })).toBeDisabled();
});

// Self-hosters may have the new look off: the list and the user page must render the same on any host.
for (const w of [390, 1280] as const) {
  test(`new look off: the list and the user page render and pass axe · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    await open(page, "/admin/users", false);
    await expect(page.locator("html")).not.toHaveAttribute("data-ui", "next");
    await expect(page.locator("[data-admin-users]")).toBeVisible();
    await expect(page.locator("[data-user-row=u3]")).toContainText("Needs re-auth");
    await expect(page.locator("[data-no-data-alert]")).toHaveCount(1);
    let ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 2).map((n) => n.target.join(" ")).join(" | ")}`)).toEqual([]);
    await page.getByRole("link", { name: "Open Quiet" }).click();
    await expect(page).toHaveURL(/\/admin\/users\/u2$/);
    await expect(onPage(page, "u2").getByRole("heading", { name: "Quiet", level: 1 })).toBeVisible();
    await expect(onPage(page, "u2").locator("[data-user-actions]")).toBeVisible();
    await expect(onPage(page, "u2").locator("[data-user-audit]")).toBeVisible();
    ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 2).map((n) => n.target.join(" ")).join(" | ")}`)).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
    await page.screenshot({ path: path.join(SHOTS, `admin-user-page-flagoff-${w}.png`), fullPage: true });
    await ctx.close();
  });
}
