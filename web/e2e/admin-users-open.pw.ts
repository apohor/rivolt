import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// On the admin site (admin_only, a long list): clicking a user opens their page (the whole row or card,
// not just the name), and Back returns to the list.
const H = 3_600_000;
const ago = (ms: number) => new Date(e2eNow() - ms).toISOString();
const mk = (i: number) => ({ id: `u${i}`, username: `user${i}@example.com`, email: `user${i}@example.com`, display_name: `User ${i}`, role: i === 0 ? "admin" : "user", disabled: false, created_at: ago((40 + i) * 24 * H), vehicle_count: 1, drive_count: 2, import_count: 0, rivian_connected: true, needs_reauth: false, last_seen_at: ago((i + 1) * H) });
const USERS = Array.from({ length: 16 }, (_, i) => mk(i));

async function open(page: Page) {
  const writes: string[] = [];
  await stubApi(page);
  await page.route("**/api/config", (r) =>
    r.fulfill({ json: { features: { trip_planner_enabled: true, signup_enabled: true, admin_only: true, rivian_actions: false }, tiles: { url: "", chargers_url: "" }, valhalla: { path: "" }, impersonation: { disabled: true } } }),
  );
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: USERS } }));
  await page.route("**/api/admin/users/*", (r) => {
    if (r.request().method() !== "GET") { writes.push(r.request().method()); return r.fulfill({ json: { ok: true } }); }
    const id = new URL(r.request().url()).pathname.split("/").pop();
    const u = USERS.find((x) => x.id === id);
    return r.fulfill({ json: { ...u, onboarding_completed: true, updated_at: ago(H), active_sessions: 0, drive_miles_total: 1, charge_count: 1, charge_kwh_total: 1, sample_count: 1, vehicles: [] } });
  });
  await page.route("**/api/admin/users/*/disabled", (r) => { writes.push("disabled"); return r.fulfill({ json: { ok: true } }); });
  await page.route("**/api/admin/audit**", (r) => r.fulfill({ json: { items: [], next_before: null } }));
  await page.goto("/admin/users");
  await expect(page.locator("[data-admin-users]")).toBeVisible();
  return writes;
}
const userPage = (page: Page, id: string) => page.locator(`[data-user-page=${id}]`);

test("1280: clicking anywhere on a row opens that user's page; Back returns", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  await open(page);
  // A plain cell, not the name link.
  await page.locator("[data-user-row=u12]").locator("[data-col=rivian]").click();
  await expect(page).toHaveURL(/\/admin\/users\/u12$/);
  await expect(userPage(page, "u12")).toContainText("user12@example.com");
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/users$/);
  // The name link opens too.
  await page.getByRole("link", { name: "Open User 7" }).click();
  await expect(page).toHaveURL(/\/admin\/users\/u7$/);
  await expect(userPage(page, "u7")).toBeVisible();
  await ctx.close();
});

test("390: tapping a card opens that user's page", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 800 }, hasTouch: true });
  const page = await ctx.newPage();
  await open(page);
  await expect(page.locator("[data-users-head]")).toBeHidden(); // stacked rows on a phone
  // The body of the card, not a button.
  await page.locator("[data-user-row=u11]").locator("[data-col=rivian]").tap();
  await expect(page).toHaveURL(/\/admin\/users\/u11$/);
  await expect(userPage(page, "u11")).toBeVisible();
  await page.goBack();
  await page.getByRole("link", { name: "Open User 3" }).tap();
  await expect(page).toHaveURL(/\/admin\/users\/u3$/);
  await ctx.close();
});

test("a row's own controls keep their meaning: Disable does not open the user", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  const writes = await open(page);
  await page.locator("[data-user-row=u5]").getByRole("button", { name: "Disable User 5" }).click();
  await expect.poll(() => writes).toContain("disabled");
  await expect(page).toHaveURL(/\/admin\/users$/); // still on the list
  await ctx.close();
});
