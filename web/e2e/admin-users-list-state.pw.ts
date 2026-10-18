import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The Users list keeps its search and filter in the URL (so Back, "All users", a reload and a shared
// link return to the same view), and adding a user is an on-demand form.
const H = 3_600_000;
const D = 24 * H;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const base = { disabled: false, vehicle_count: 1, drive_count: 4, import_count: 0, rivian_connected: true, needs_reauth: false, created_at: ago(30 * D), last_seen_at: ago(H) };
const USERS = [
  { ...base, id: "a1", username: "owner@example.com", email: "owner@example.com", display_name: "Owner", role: "admin" },
  { ...base, id: "u2", username: "ann@example.com", email: "ann@example.com", display_name: "Ann", role: "user", needs_reauth: true, needs_reauth_at: ago(2 * H) },
  { ...base, id: "u3", username: "bob@example.com", email: "bob@example.com", display_name: "Bob", role: "user" },
  { ...base, id: "u4", username: "anna@example.com", email: "anna@example.com", display_name: "Anna", role: "user", needs_reauth: true, needs_reauth_at: ago(5 * H) },
];

async function open(page: Page, url = "/admin/users", createPassword: string | null = null) {
  const created: unknown[] = [];
  await stubApi(page, { uiNext: true });
  await page.route("**/api/admin/users", (r) => {
    if (r.request().method() === "POST") {
      created.push(r.request().postDataJSON());
      return r.fulfill({ json: { id: "u9", ...(createPassword ? { password: createPassword } : {}) } });
    }
    return r.fulfill({ json: { users: USERS } });
  });
  await page.route("**/api/admin/users/*", (r) => {
    const id = new URL(r.request().url()).pathname.split("/").pop();
    const u = USERS.find((x) => x.id === id);
    return r.fulfill({ json: { ...u, onboarding_completed: true, updated_at: ago(H), active_sessions: 0, drive_miles_total: 1, charge_count: 1, charge_kwh_total: 1, sample_count: 1, vehicles: [] } });
  });
  await page.route("**/api/admin/audit**", (r) => r.fulfill({ json: { items: [], next_before: null } }));
  await page.goto(url);
  await expect(page.locator("[data-admin-users]")).toBeVisible();
  return created;
}
const ids = (page: Page) => page.locator("[data-user-row]").evaluateAll((els) => els.map((e) => e.getAttribute("data-user-row")));

test("search and filter are in the URL and survive opening a user, All users, Back and a reload", async ({ page }) => {
  await open(page);
  await expect.poll(() => ids(page)).toEqual(["a1", "u2", "u3", "u4"]);
  await page.getByRole("searchbox", { name: "Search users" }).fill("ann");
  await expect(page).toHaveURL(/\/admin\/users\?q=ann$/);
  await page.getByRole("button", { name: /^Re-auth/ }).click();
  await expect(page).toHaveURL(/q=ann/);
  await expect(page).toHaveURL(/f=reauth/);
  await expect.poll(() => ids(page)).toEqual(["u2", "u4"]);
  // Open a user, come back with "All users": same view.
  await page.getByRole("link", { name: "Open Anna" }).click();
  await expect(page).toHaveURL(/\/admin\/users\/u4$/);
  await page.getByRole("link", { name: "← All users" }).click();
  await expect(page).toHaveURL(/\/admin\/users\?q=ann&f=reauth$/);
  await expect.poll(() => ids(page)).toEqual(["u2", "u4"]);
  await expect(page.getByRole("searchbox", { name: "Search users" })).toHaveValue("ann");
  await expect(page.getByRole("button", { name: /^Re-auth/ })).toHaveAttribute("aria-pressed", "true");
  // The browser's Back does the same.
  await page.locator("[data-user-row=u2]").locator("[data-col=rivian]").click();
  await expect(page).toHaveURL(/\/admin\/users\/u2$/);
  await page.goBack();
  await expect(page).toHaveURL(/q=ann&f=reauth/);
  await expect.poll(() => ids(page)).toEqual(["u2", "u4"]);
  // A reload, and a shared link.
  await page.reload();
  await expect.poll(() => ids(page)).toEqual(["u2", "u4"]);
  await page.goto("/admin/users?f=reauth");
  await expect.poll(() => ids(page)).toEqual(["u2", "u4"]);
  // Clearing goes back to a clean URL, and typing doesn't pile up history entries.
  await page.getByRole("button", { name: /^All/ }).click();
  await page.getByRole("searchbox", { name: "Search users" }).fill("");
  await expect(page).toHaveURL(/\/admin\/users$/);
  await expect.poll(async () => (await ids(page)).length).toBe(4);
});

test("a junk filter in the URL falls back to All", async ({ page }) => {
  await open(page, "/admin/users?f=bogus");
  await expect.poll(async () => (await ids(page)).length).toBe(4);
  await expect(page.getByRole("button", { name: /^All/ })).toHaveAttribute("aria-pressed", "true");
});

test("deleting from a user's page returns to the list view you came from", async ({ page }) => {
  await open(page);
  await page.route("**/api/admin/users/u3", (r) => (r.request().method() === "DELETE" ? r.fulfill({ json: { ok: true, idp: "ok" } }) : r.fallback()));
  await page.getByRole("searchbox", { name: "Search users" }).fill("bob");
  await page.getByRole("link", { name: "Open Bob" }).click();
  await page.locator("[data-user-actions]").getByRole("button", { name: /Delete user/ }).click();
  await page.locator("[data-typed-confirm]").getByLabel(/Type/).fill("bob@example.com");
  await page.locator("[data-typed-confirm]").getByRole("button", { name: "Delete user" }).click();
  await expect(page).toHaveURL(/\/admin\/users\?q=bob$/);
});

test("Add user: closed by default, opens on demand, a plain create closes it", async ({ page }) => {
  const created = await open(page);
  const toggle = page.getByRole("button", { name: "Add user" });
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByPlaceholder("email", { exact: true })).toHaveCount(0);
  await toggle.click();
  await expect(page.getByRole("button", { name: "Close" })).toHaveAttribute("aria-expanded", "true");
  await page.getByPlaceholder("email", { exact: true }).fill("new@example.com");
  await page.getByRole("button", { name: "Create user" }).click();
  await expect.poll(() => created).toHaveLength(1);
  expect(created[0]).toMatchObject({ email: "new@example.com", role: "user" });
  await expect(page.getByPlaceholder("email", { exact: true })).toHaveCount(0); // closed again
});

test("Add user: a one-time password keeps the form open until it is dismissed", async ({ page }) => {
  await open(page, "/admin/users", "Tmp-Pass-123");
  await page.getByRole("button", { name: "Add user" }).click();
  await page.getByPlaceholder("email", { exact: true }).fill("new@example.com");
  await page.getByRole("button", { name: "Create user" }).click();
  await expect(page.getByText("Tmp-Pass-123")).toBeVisible();
  await expect(page.getByPlaceholder("email", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Dismiss" }).click();
  await expect(page.getByPlaceholder("email", { exact: true })).toHaveCount(0);
});

for (const w of [390, 1280] as const) {
  test(`list with a filter and the add form open · axe, no overflow · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    await open(page, "/admin/users?f=reauth");
    await page.getByRole("button", { name: "Add user" }).click();
    expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
    const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 2).map((n) => n.target.join(" ")).join(" | ")}`)).toEqual([]);
    await ctx.close();
  });
}

test("typing quickly in the search box loses no characters, and the URL follows once", async ({ page }) => {
  await open(page);
  const box = page.getByRole("searchbox", { name: "Search users" });
  await box.pressSequentially("anna", { delay: 15 });
  await expect(box).toHaveValue("anna");
  await expect.poll(() => ids(page)).toEqual(["u4"]);
  await expect(page).toHaveURL(/\/admin\/users\?q=anna$/);
  // Back from a user restores what was typed, even when opened before the URL caught up.
  await box.fill("bo");
  await page.getByRole("link", { name: "Open Bob" }).click();
  await page.getByRole("link", { name: "← All users" }).click();
  await expect(page).toHaveURL(/\/admin\/users\?q=bo$/);
  await expect(page.getByRole("searchbox", { name: "Search users" })).toHaveValue("bo");
});
