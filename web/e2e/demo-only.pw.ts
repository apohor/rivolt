import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// The standalone demo deployment (features.demo_only + app_url in
// /api/config): "/" lands on /demo, auth and app routes redirect there, and
// every sign-in CTA points at the real app. Normal deployments are
// unaffected.

async function setup(page: import("@playwright/test").Page) {
  await stubApi(page, { demoOnly: true, signedIn: false });
  const calls: string[] = [];
  page.on("request", (r) => {
    const p = new URL(r.url()).pathname;
    if (p.startsWith("/api/")) calls.push(p);
  });
  return calls;
}

for (const from of ["/", "/login", "/signup", "/onboarding", "/auth/hydra/login", "/money", "/settings", "/admin", "/trips/plan"]) {
  test(`demo-only: ${from} goes to /demo`, async ({ page }) => {
    const calls = await setup(page);
    await page.goto(from);
    await expect(page).toHaveURL(/\/demo$/);
    await expect(page.getByText(/Sample truck\./).first()).toBeVisible();
    // No auth surface was ever touched.
    expect(calls).not.toContain("/api/auth/me");
    expect(calls.filter((c) => c.startsWith("/api/settings/rivian"))).toEqual([]);
  });
}

test("demo-only: deep demo routes stay put", async ({ page }) => {
  await setup(page);
  await page.goto("/demo/activity");
  await expect(page).toHaveURL(/\/demo\/activity$/);
});

test("demo-only: every Connect CTA points at the real app", async ({ page }) => {
  await setup(page);
  await page.goto("/demo");
  await expect(page.getByRole("link", { name: "Connect with a driver account" })).toHaveAttribute("href", "https://rivolt.dev/signup");
  await page.goto("/demo/trips/plan");
  await expect(page.getByTestId("connect-hint").first().getByRole("link")).toHaveAttribute("href", "https://rivolt.dev/signup");
  await page.goto("/demo/settings");
  await expect(page.getByTestId("connect-hint").first().getByRole("link")).toHaveAttribute("href", /^https:\/\/rivolt\.dev\//);
});

test("demo-only: the server's signup_enabled=false (forced there) still sends CTAs to the real app's /signup", async ({ page }) => {
  await stubApi(page, { demoOnly: true, signedIn: false, signupDisabled: true });
  await page.goto("/demo");
  await expect(page.getByRole("link", { name: "Connect with a driver account" })).toHaveAttribute("href", "https://rivolt.dev/signup");
});

test("normal deployment: /login is still the login page and the CTA stays in-app", async ({ page }) => {
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/login");
  await expect(page).toHaveURL(/\/login$/);
  await page.goto("/demo");
  await expect(page.getByRole("link", { name: "Connect with a driver account" })).toHaveAttribute("href", "/signup");
});

// The standalone demo counts where visitors come from: one demo_view per
// page load with the sanitised utm_source and referrer host, read from the
// entry URL before the redirect to /demo drops the query.
test("demo-only: a visit with utm_source sends one demo_view, source only", async ({ page }) => {
  await stubApi(page, { demoOnly: true, signedIn: false });
  const posts: { anon_id: string; events: { name: string; props?: Record<string, string> }[] }[] = [];
  await page.route("**/api/events", (r) => {
    posts.push(r.request().postDataJSON());
    return r.fulfill({ status: 204 });
  });
  await page.goto("/?utm_source=Reddit&utm_medium=post&token=secret");
  await expect(page).toHaveURL(/\/demo$/);
  await expect(page.getByText(/Sample truck\./).first()).toBeVisible();
  await page.goto("/demo/activity", { waitUntil: "load" }); // a second page load is a second visit, with no source
  await expect.poll(() => posts.length).toBe(2);
  expect(posts[0].events).toEqual([{ name: "demo_view", props: { utm_source: "reddit" } }]);
  expect(posts[1].events).toEqual([{ name: "demo_view", props: {} }]);
  expect(JSON.stringify(posts)).not.toContain("secret");
});

test("demo-only: Global Privacy Control sends no demo_view", async ({ page }) => {
  await stubApi(page, { demoOnly: true, signedIn: false });
  await page.addInitScript(() => Object.defineProperty(navigator, "globalPrivacyControl", { value: true, configurable: true }));
  const posts: unknown[] = [];
  await page.route("**/api/events", (r) => {
    posts.push(r.request().postDataJSON());
    return r.fulfill({ status: 204 });
  });
  await page.goto("/demo?utm_source=reddit");
  await expect(page.getByText(/Sample truck\./).first()).toBeVisible();
  await page.waitForTimeout(1000);
  expect(posts).toEqual([]);
});
