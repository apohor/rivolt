import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// The admin site, signed out: a visitor who got through the edge but has no Rivolt session is sent to
// sign in and comes back to /admin, instead of a bare "not authorized".
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
const HEALTH = {
  generated_at: new Date().toISOString(), version: "0.36.21",
  alerts: { status: "ok", items: [] }, kill_switch: { status: "ok", paused: false },
  rivian: { status: "ok", window: "24h", results: {}, error_rate: null, breaker_state: 0, breaker_trips: {}, rate_limited: {} },
  vehicles: { status: "ok", leased: 0, streaming: 0 }, disk: { status: "ok", used_pct: 30 }, backup: { status: "ok", age_seconds: 3600 },
};

async function setup(page: Page, role: "admin" | "user" = "admin") {
  let signedIn = false;
  const started: string[] = [];
  await stubApi(page);
  await page.route("**/api/config", (r) =>
    r.fulfill({ json: { features: { trip_planner_enabled: true, signup_enabled: true, admin_only: true }, tiles: { url: "", chargers_url: "" }, valhalla: { path: "" }, impersonation: { disabled: true } } }),
  );
  await page.route("**/api/auth/me", (r) =>
    signedIn ? r.fulfill({ json: { user_id: "u-owner", username: "owner@example.com", role, onboarding_completed: true } }) : r.fulfill({ status: 401, json: { error: "unauthenticated" } }),
  );
  await page.route("**/api/auth/oidc/", (r) => r.fulfill({ json: [{ name: "google", display_name: "Google", start_url: "/api/auth/oidc/google/start" }] }));
  // The identity provider round trip: starting it records the return path; the browser comes back signed in.
  await page.route("**/api/auth/oidc/google/start**", (r) => {
    started.push(new URL(r.request().url()).searchParams.get("return") ?? "");
    signedIn = true;
    return r.fulfill({ status: 302, headers: { location: new URL(r.request().url()).searchParams.get("return") ?? "/" } });
  });
  await page.route("**/api/admin/health-summary", (r) => r.fulfill({ json: HEALTH }));
  await page.route("**/api/admin/users", (r) => r.fulfill({ json: { users: [] } }));
  return { started };
}

for (const w of [390, 1280] as const) {
  test(`signed out on /admin → sign in → back on /admin · ${w}px`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: 900 } });
    const page = await ctx.newPage();
    const { started } = await setup(page);
    await page.goto("/admin");
    await expect(page).toHaveURL(/\/login\?next=%2Fadmin$|\/login\?next=\/admin$/);
    await expect(page.getByText("Not authorized")).toHaveCount(0);
    await expect(page.getByRole("button", { name: /Continue with Google/ })).toBeVisible();
    await page.screenshot({ path: path.join(SHOTS, `admin-signin-${w}.png`) });
    await page.getByRole("button", { name: /Continue with Google/ }).click();
    await expect(page).toHaveURL(/\/admin$/);
    expect(started).toEqual(["/admin"]);
    await expect(page.locator("[data-admin-only-shell]")).toBeVisible();
    await expect(page.locator("[data-admin-home]")).toBeVisible();
    await ctx.close();
  });
}

test("any other path signed out also lands on sign-in, then /admin", async ({ page }) => {
  await setup(page);
  await page.goto("/drives");
  await expect(page).toHaveURL(/\/login\?next=%2Fadmin$|\/login\?next=\/admin$/);
});

test("signed in but not an admin still gets the admin page's own 'not authorized'", async ({ page }) => {
  await setup(page, "user");
  await page.goto("/login?next=/admin"); // sign in
  await page.getByRole("button", { name: /Continue with Google/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.getByText("Not authorized")).toBeVisible();
});

test("signed in as an admin goes straight in", async ({ page }) => {
  await setup(page);
  await page.goto("/login?next=/admin");
  await page.getByRole("button", { name: /Continue with Google/ }).click();
  await expect(page.locator("[data-admin-home]")).toBeVisible();
  await page.goto("/admin");
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.locator("[data-admin-home]")).toBeVisible();
});
