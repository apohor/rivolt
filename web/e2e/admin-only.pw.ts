import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// The admin site (features.admin_only): the new admin look is on without /api/ledger/enabled, the
// shell has no user navigation, no status pill and no user-side fetches, and every non-admin path
// goes to /admin. While viewing as a user, the user pages render as on rivolt.dev.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ok = { status: "ok" };
const HEALTH = {
  generated_at: new Date(e2eNow()).toISOString(), version: "0.36.21",
  alerts: { ...ok, items: [] }, kill_switch: { ...ok, paused: false },
  rivian: { ...ok, window: "24h", results: { success: 10 }, error_rate: 0, breaker_state: 0, breaker_trips: {}, rate_limited: {} },
  vehicles: { ...ok, leased: 1, streaming: 1 }, disk: { ...ok, used_pct: 30 }, backup: { ...ok, age_seconds: 3600 },
};

async function open(page: Page, o: { adminOnly?: boolean | "absent"; url?: string; impersonate?: boolean } = {}) {
  const { adminOnly = true, url = "/", impersonate = false } = o;
  const apiPaths: string[] = [];
  await stubApi(page);
  await page.route("**/api/config", (r) =>
    r.fulfill({ json: { features: { trip_planner_enabled: true, signup_enabled: true, ...(adminOnly === "absent" ? {} : { admin_only: adminOnly }) }, tiles: { url: "", chargers_url: "" }, valhalla: { path: "" }, impersonation: { disabled: false } } }),
  );
  // The admin site has none of this: it answers 404, and the test fails if the SPA asks.
  await page.route("**/api/ledger/enabled", (r) => r.fulfill({ status: 404, json: { error: "not found" } }));
  await page.route("**/api/admin/health-summary", (r) => r.fulfill({ json: HEALTH }));
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: [] } }));
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/")) apiPaths.push(u.pathname);
  });
  if (impersonate) {
    await page.addInitScript(() => sessionStorage.setItem("rivolt.impersonate", JSON.stringify({ id: "22222222-2222-2222-2222-222222222222", email: "ann@example.com" })));
  }
  await page.goto(url);
  return apiPaths;
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`admin site: new look, no user shell, no user fetches · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      const apiPaths = await open(page);
      await expect(page).toHaveURL(/\/admin$/);
      await expect(page.locator("[data-admin-only-shell]")).toBeVisible();
      await expect(page.locator("html")).toHaveAttribute("data-ui", "next");
      await expect(page.getByRole("tab", { name: "Home" }).or(page.getByRole("button", { name: "Home" }))).toBeVisible(); // the new admin look
      await expect(page.locator("[data-admin-home]")).toBeVisible();
      // No user navigation, no status pill, no footer links to pages that do not exist here.
      for (const name of ["Overview", "Live", "Drives", "Charges", "Plan", "Settings", "Now", "Activity", "Money", "Truck"]) {
        await expect(page.getByRole("link", { name, exact: true }), name).toHaveCount(0);
      }
      await expect(page.getByRole("navigation", { name: "Primary" })).toHaveCount(0);
      await expect(page.getByText("checking…")).toHaveCount(0);
      await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      await page.waitForTimeout(500);
      // Only what the admin site answers: /api/admin, /api/auth, /api/health, /api/config.
      const bad = apiPaths.filter((p) => !/^\/api\/(admin\/|auth\/|health$|config$)/.test(p));
      expect(bad, bad.join("\n")).toEqual([]);
      expect(apiPaths).not.toContain("/api/ledger/enabled");
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `admin-only-${w}-${theme}.png`), fullPage: true });
      await ctx.close();
    });
  }
}

test("every non-admin path goes to /admin; sign-in stays", async ({ page }) => {
  for (const p of ["/drives", "/settings", "/live", "/money", "/charges/5", "/trips/plan", "/truck", "/activity"]) {
    await page.goto("about:blank");
    await open(page, { url: p });
    await expect(page, p).toHaveURL(/\/admin$/);
  }
});

test("/login is still the login page on the admin site", async ({ page }) => {
  await open(page, { url: "/login" });
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.locator("[data-admin-only-shell]")).toHaveCount(0);
});

test("viewing as a user: the user pages render as on rivolt.dev", async ({ page }) => {
  await open(page, { impersonate: true });
  await expect(page).toHaveURL(/\/$/); // not sent to /admin
  await expect(page.locator("[data-admin-only-shell]")).toHaveCount(0);
  await expect(page.getByText("Viewing as")).toBeVisible();
  await expect(page.getByRole("link", { name: /Activity/ }).first()).toBeVisible(); // the tab shell
});

test("without the flag (an older server, or rivolt.dev) nothing changes", async ({ page }) => {
  await open(page, { adminOnly: "absent" });
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator("[data-admin-only-shell]")).toHaveCount(0);
  await expect(page.getByRole("link", { name: /Activity/ }).first()).toBeVisible(); // the tab shell
});
