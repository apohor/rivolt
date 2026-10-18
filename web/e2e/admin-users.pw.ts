import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Admin > Users (new look): status columns, the red "leased, no data" rule, the purge countdown,
// filters and search, row actions, and a typed confirm before a delete.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const H = 3_600_000;
const D = 24 * H;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const base = { disabled: false, vehicle_count: 1, drive_count: 4, import_count: 0, rivian_connected: true, needs_reauth: false };
const USERS = [
  { ...base, id: "a1", username: "admin@example.com", email: "admin@example.com", display_name: "Owner", role: "admin", created_at: ago(200 * D), last_seen_at: ago(60_000), last_data_at: ago(5 * 60_000), leased: true, receipts_opt_in: true },
  { ...base, id: "u1", username: "ok@example.com", email: "ok@example.com", role: "user", created_at: ago(40 * D), last_seen_at: ago(3 * H), last_data_at: ago(10 * 60_000), leased: true, receipts_opt_in: true },
  { ...base, id: "u2", username: "quiet@example.com", email: "quiet@example.com", role: "user", created_at: ago(30 * D), last_seen_at: ago(2 * D), last_data_at: ago(5 * H), leased: true, receipts_opt_in: false },
  { ...base, id: "u3", username: "reauth@example.com", email: "reauth@example.com", role: "user", needs_reauth: true, needs_reauth_at: ago(26 * H), created_at: ago(70 * D), last_seen_at: ago(9 * D), last_data_at: ago(26 * H), leased: false, receipts_opt_in: false },
  { ...base, id: "u4", username: "idle@example.com", email: "idle@example.com", role: "user", vehicle_count: 0, rivian_connected: false, created_at: ago(105 * D), last_seen_at: null, last_data_at: null, leased: false, receipts_opt_in: false },
  { ...base, id: "u5", username: "warned@example.com", email: "warned@example.com", role: "user", vehicle_count: 0, rivian_connected: false, created_at: ago(112 * D), last_seen_at: ago(90 * D), last_data_at: null, leased: false, receipts_opt_in: false, purge_warned_at: ago(22 * D) },
];

async function open(page: Page, users: unknown[] = USERS) {
  await stubApi(page, { uiNext: true });
  await page.route("**/api/admin/users", (r) => (r.request().method() === "GET" ? r.fulfill({ json: { users } }) : r.fallback()));
  await page.route("**/api/admin/users/*", (r) => {
    const id = new URL(r.request().url()).pathname.split("/").pop();
    if (r.request().method() === "GET") {
      const u = (users as typeof USERS).find((x) => x.id === id);
      return r.fulfill({ json: { ...u, onboarding_completed: true, updated_at: ago(D), rivian_session_at: ago(D), active_sessions: 1, drive_miles_total: 10, charge_count: 2, charge_kwh_total: 5, sample_count: 3, import_count: 0, vehicles: [] } });
    }
    return r.fallback();
  });
  await page.goto("/admin#users");
  await expect(page.locator("[data-admin-users]")).toBeVisible();
}
const rowOf = (page: Page, id: string) => page.locator(`[data-user-row=${id}]`);

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`admin users table · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page);
      // Status columns.
      await expect(rowOf(page, "u1").locator("[data-col=rivian]")).toHaveText("Connected");
      await expect(rowOf(page, "u3").locator("[data-col=rivian]")).toContainText("Needs re-auth");
      await expect(rowOf(page, "u3").locator("[data-col=rivian]")).toContainText("since 1d ago");
      await expect(rowOf(page, "u4").locator("[data-col=rivian]")).toHaveText("Not connected");
      // The red rule: leased, connected, no data for over two hours. Only that one.
      await expect(rowOf(page, "u2").locator("[data-no-data-alert]")).toHaveText("Leased, no data 2h+");
      await expect(page.locator("[data-no-data-alert]")).toHaveCount(1);
      await expect(rowOf(page, "u2").locator("[data-col=last-data]")).toContainText("5h ago");
      // Purge countdown: day 105 → 15 days; warned account is called out; connected users show none.
      await expect(rowOf(page, "u4").locator("[data-col=purge]")).toHaveText("Purge in 15d");
      await expect(rowOf(page, "u5").locator("[data-col=purge]")).toContainText("Purge in 8d");
      await expect(rowOf(page, "u5").locator("[data-col=purge]")).toContainText("warned");
      await expect(rowOf(page, "u1").locator("[data-col=purge]")).toHaveText("—");
      await expect(rowOf(page, "u1").locator("[data-col=receipts]")).toHaveText("Receipts on");
      // The page itself never scrolls sideways. A phone gets stacked cards with every action inside
      // the screen; a desktop gets the table.
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      if (w === 390) {
        await expect(page.locator("[data-admin-users-cards]")).toBeVisible();
        await expect(page.locator("[data-admin-users] table")).toHaveCount(0);
        for (const b of await rowOf(page, "u2").getByRole("button").all()) {
          const box = (await b.boundingBox())!;
          expect(box.x).toBeGreaterThanOrEqual(0);
          expect(box.x + box.width).toBeLessThanOrEqual(390);
        }
        await expect(rowOf(page, "u2").getByRole("button", { name: "Delete quiet@example.com" })).toBeVisible();
        await expect(rowOf(page, "u3").getByRole("button", { name: /^Refresh Rivian session/ })).toBeVisible();
      } else {
        await expect(page.locator("[data-admin-users] table")).toBeVisible();
      }
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `admin-users-${w}-${theme}.png`), fullPage: true });
      await ctx.close();
    });
  }
}

test("filter chips and search", async ({ page }) => {
  await open(page);
  const ids = async () => page.locator("[data-user-row]").evaluateAll((els) => els.map((e) => e.getAttribute("data-user-row")));
  expect(await ids()).toHaveLength(6);
  await page.getByRole("button", { name: /^Needs attention/ }).click();
  expect(await ids()).toEqual(["u2", "u3", "u4", "u5"]);
  await page.getByRole("button", { name: /^Re-auth/ }).click();
  expect(await ids()).toEqual(["u3"]);
  await page.getByRole("button", { name: /^No data/ }).click();
  expect(await ids()).toEqual(["u2"]);
  await page.getByRole("button", { name: /^All/ }).click();
  await page.getByRole("searchbox", { name: "Search users" }).fill("WARNED");
  expect(await ids()).toEqual(["u5"]);
  await page.getByRole("searchbox", { name: "Search users" }).fill("nobody");
  await expect(page.getByText("No users match.")).toBeVisible();
});

test("row actions: sync reports, disable posts, delete needs the typed name", async ({ page }) => {
  await open(page);
  const calls: string[] = [];
  await page.route("**/api/admin/users/u2/sync-rivian", (r) => { calls.push("sync"); return r.fulfill({ json: { ok: true, vehicle_count: 2 } }); });
  await page.route("**/api/admin/users/u2/disabled", (r) => { calls.push(`disabled ${r.request().postData()}`); return r.fulfill({ json: { ok: true } }); });
  await page.route("**/api/admin/users/u2", (r) => {
    if (r.request().method() !== "DELETE") return r.fallback();
    calls.push("delete");
    return r.fulfill({ json: { ok: true, idp: "ok" } });
  });
  await rowOf(page, "u2").getByRole("button", { name: "Sync quiet@example.com" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Synced: 2 vehicles on file for quiet@example.com." })).toBeVisible();
  await rowOf(page, "u2").getByRole("button", { name: "Disable quiet@example.com" }).click();
  await expect.poll(() => calls.some((c) => c.startsWith("disabled"))).toBe(true);
  // Delete: asks, stays disabled until the exact name is typed, Cancel sends nothing.
  await rowOf(page, "u2").getByRole("button", { name: "Delete quiet@example.com" }).click();
  const dlg = page.locator("[data-typed-confirm]");
  await expect(dlg.getByRole("button", { name: "Delete user" })).toBeDisabled();
  await dlg.getByLabel(/Type/).fill("quiet@example");
  await expect(dlg.getByRole("button", { name: "Delete user" })).toBeDisabled();
  await dlg.getByRole("button", { name: "Cancel" }).click();
  expect(calls.includes("delete")).toBe(false);
  await rowOf(page, "u2").getByRole("button", { name: "Delete quiet@example.com" }).click();
  await dlg.getByLabel(/Type/).fill("quiet@example.com");
  await dlg.getByRole("button", { name: "Delete user" }).click();
  await expect.poll(() => calls.includes("delete")).toBe(true);
});

test("the last admin can be neither disabled nor deleted", async ({ page }) => {
  await open(page);
  await expect(rowOf(page, "a1").getByRole("button", { name: "Disable Owner" })).toBeDisabled();
  await expect(rowOf(page, "a1").getByRole("button", { name: "Delete Owner" })).toBeDisabled();
  await expect(rowOf(page, "a1").getByRole("button", { name: /View as/ })).toHaveCount(0);
});

test("columns the API does not send yet show a dash, not a wrong answer", async ({ page }) => {
  const bare = USERS.map((u) => { const { last_data_at, leased, receipts_opt_in, purge_warned_at, ...rest } = u as Record<string, unknown>; void last_data_at; void leased; void receipts_opt_in; void purge_warned_at; return rest; });
  await open(page, bare);
  await expect(rowOf(page, "u2").locator("[data-col=last-data]")).toHaveText("—");
  await expect(rowOf(page, "u2").locator("[data-col=receipts]")).toHaveText("Receipts —");
  await expect(page.locator("[data-no-data-alert]")).toHaveCount(0);
});

