import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// Admin > Audit (new look, read-only, paged, filterable) and the per-user history on the user detail.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const H = 3_600_000;
const ago = (ms: number) => new Date(e2eNow() - ms).toISOString();
const ADMIN = { id: "11111111-1111-1111-1111-111111111111", username: "owner@example.com", email: "owner@example.com", display_name: "Owner", role: "admin", disabled: false, created_at: ago(900 * H), vehicle_count: 1, drive_count: 1, import_count: 0, rivian_connected: true, needs_reauth: false };
const USER = { ...ADMIN, id: "22222222-2222-2222-2222-222222222222", username: "ann@example.com", email: "ann@example.com", display_name: "Ann", role: "user" };

const entry = (id: number, action: string, extra: Record<string, unknown> = {}) => ({
  id, at: ago(id * H), actor_user_id: ADMIN.id, action, target_type: action.startsWith("user.") ? "user" : "flag", target_id: "x",
  details: {}, ip: "203.0.113.7", outcome: "ok", status: 200, ...extra,
});
const ALL = [
  entry(1, "flag.kill_switch", { details: { paused: true, before_paused: false, reason: "Rivian is blocking us" }, access_email: "owner@example.com" }),
  entry(2, "user.set_disabled", { target_user_id: USER.id, target_id: USER.id, details: { disabled: true, before_disabled: false } }),
  entry(3, "user.delete", { target_user_id: USER.id, target_id: USER.id, outcome: "failed", status: 409 }),
  entry(4, "settings.ai", { details: { fields: ["anthropic_key", "model"] } }),
  entry(5, "impersonation.start", { target_user_id: USER.id, target_id: USER.id }),
  entry(6, "user.sync_rivian", { target_user_id: USER.id, target_id: USER.id, outcome: null, status: undefined }),
];

async function open(page: Page, o: { pageSize?: number } = {}) {
  const seen: string[] = [];
  await stubApi(page, { uiNext: true });
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: [ADMIN, USER] } }));
  await page.route("**/api/admin/users/*", (r) =>
    r.request().method() === "GET"
      ? r.fulfill({ json: { ...USER, onboarding_completed: true, updated_at: ago(H), active_sessions: 0, drive_miles_total: 0, charge_count: 0, charge_kwh_total: 0, sample_count: 0, vehicles: [] } })
      : r.fallback(),
  );
  await page.route("**/api/admin/audit**", (r) => {
    const u = new URL(r.request().url());
    seen.push(u.search);
    const { action, actor, target_user: target } = Object.fromEntries(u.searchParams);
    const before = Number(u.searchParams.get("before") ?? 0);
    const limit = Number(u.searchParams.get("limit") ?? 50);
    const size = o.pageSize ?? limit;
    let rows = ALL.filter((e) => (!action || e.action === action) && (!actor || e.actor_user_id === actor) && (!target || e.target_user_id === target));
    rows = rows.filter((e) => !before || e.id > before).slice(0, size);
    const last = rows.at(-1);
    const more = last && ALL.filter((e) => (!action || e.action === action) && (!target || e.target_user_id === target)).some((e) => e.id > last.id);
    return r.fulfill({ json: { items: rows, next_before: more && last ? last.id : null } });
  });
  await page.goto("/admin#audit");
  await expect(page.locator("[data-admin-audit]")).toBeVisible();
  return seen;
}
const ids = (page: Page) => page.locator("[data-audit-row]").evaluateAll((els) => els.map((e) => e.getAttribute("data-audit-row")));

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`audit · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page);
      expect(await ids(page)).toEqual(["1", "2", "3", "4", "5", "6"]);
      const first = page.locator("[data-audit-row='1']");
      await expect(first).toContainText("Kill switch");
      await expect(first).toContainText("Owner");
      await expect(first).toContainText("owner@example.com · 203.0.113.7");
      await first.getByText("Details").click();
      await expect(first).toContainText("paused: false → true");
      await expect(first).toContainText("reason: Rivian is blocking us");
      await expect(page.locator("[data-audit-row='2']")).toContainText("Ann"); // About: the user's name
      await expect(page.locator("[data-audit-row='3']")).toContainText("failed (409)");
      await expect(page.locator("[data-audit-row='4']")).toContainText("anthropic_key, model"); // names only
      await expect(page.locator("[data-audit-row='6']")).toContainText("no result recorded");
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `admin-audit-${w}-${theme}.png`), fullPage: true });
      await ctx.close();
    });
  }
}

test("filters narrow the list and are sent to the API", async ({ page }) => {
  const seen = await open(page);
  await page.getByLabel("Action").selectOption("user.delete");
  await expect.poll(() => ids(page)).toEqual(["3"]);
  expect(seen.at(-1)).toContain("action=user.delete");
  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect.poll(() => ids(page)).toHaveLength(6);
  await page.getByLabel("About user").selectOption(USER.id);
  await expect.poll(() => ids(page)).toEqual(["2", "3", "5", "6"]);
  expect(seen.at(-1)).toContain(`target_user=${USER.id}`);
  await page.getByLabel("Action").selectOption("flag.signup_cap");
  await expect(page.getByText("No entries match these filters.")).toBeVisible();
});

test("paging: Load older follows the cursor until the end", async ({ page }) => {
  const seen = await open(page, { pageSize: 4 });
  expect(await ids(page)).toEqual(["1", "2", "3", "4"]);
  await page.getByRole("button", { name: "Load older" }).click();
  await expect.poll(() => ids(page)).toEqual(["1", "2", "3", "4", "5", "6"]);
  expect(seen.some((s) => s.includes("before=4"))).toBe(true);
  await expect(page.getByRole("button", { name: "Load older" })).toHaveCount(0);
  await expect(page.getByText("That is everything: 6 entries.")).toBeVisible();
});

test("read-only: nothing on the page writes", async ({ page }) => {
  const writes: string[] = [];
  page.on("request", (r) => { if (r.url().includes("/api/") && r.method() !== "GET") writes.push(r.method()); });
  await open(page);
  await page.getByLabel("Action").selectOption("user.delete");
  await page.waitForTimeout(300);
  expect(writes).toEqual([]);
});

test("the user detail shows that user's admin history", async ({ page }) => {
  const seen = await open(page);
  await page.goto(`/admin/users/${USER.id}`); // the user's own page, deep-linked
  await expect(page.locator("[data-user-audit]")).toBeVisible();
  const history = page.locator("[data-user-audit]");
  await expect(history).toContainText("Disable or enable user");
  await expect(history).toContainText("Delete user");
  expect(seen.some((s) => s.includes(`target_user=${USER.id}`) && s.includes("limit=10"))).toBe(true);
});
