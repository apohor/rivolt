import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// /signup copy now that email signup is instant, and the terms gate in front of /onboarding.

test("signup with Google/GitHub offered: instant email wording and the send-link button", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.route("**/api/auth/oidc/", (r) => r.fulfill({ json: [{ name: "google", display_name: "Google", start_url: "/api/auth/oidc/google/start" }] }));
  await page.goto("/signup");
  await expect(page.getByText("Sign up instantly with a provider below, or enter your email and we'll send you a signup link.")).toBeVisible();
  await expect(page.getByText("or sign up by email")).toBeVisible();
  await expect(page.getByRole("button", { name: "Send signup link" })).toBeVisible();
  const body = await page.locator("body").innerText();
  expect(body).not.toMatch(/manual approval|Request access|once approved/);
});

test("signup without OAuth providers says the same", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.route("**/api/auth/oidc/", (r) => r.fulfill({ json: [] }));
  await page.goto("/signup");
  await expect(page.getByText("Enter your email and we'll send you a signup link.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Send signup link" })).toBeVisible();
});

for (const [auto, heading, text] of [
  [true, "Check your inbox.", "We sent a signup link to"],
  [false, "Request received.", "We let people in as room frees up."],
] as const) {
  test(`after sending: ${auto ? "auto_approved" : "pending"}`, async ({ page }) => {
    await stubApi(page, { signedIn: false });
    await page.route("**/api/auth/oidc/", (r) => r.fulfill({ json: [] }));
    await page.route("**/api/signup/request", (r) => r.fulfill({ json: { ok: true, auto_approved: auto } }));
    await page.goto("/signup");
    await page.getByPlaceholder("you@example.com").fill("new@example.com");
    await page.getByRole("button", { name: "Send signup link" }).click();
    await expect(page.getByText(heading)).toBeVisible();
    await expect(page.getByText(text)).toBeVisible();
    const sign = page.locator("[data-signup-have-account]");
    await expect(sign).toContainText("Already have an account? Sign in");
    await expect(sign.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
    const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
  });
}

// A new user who signs up from a signup link is signed in and sent to /onboarding. The app's cached
// "who am I" was taken signed out, so the gate must be refreshed before the Rivian form can show.
async function signupFlow(page: Page) {
  let signedIn = false;
  await stubApi(page, { signedIn: false, rivian: "disconnected" });
  await page.route("**/api/signup/token/*", (r) => r.fulfill({ json: { email: "new@example.com" } }));
  await page.route("**/api/signup", (r) => {
    if (r.request().method() !== "POST") return r.fallback();
    signedIn = true;
    return r.fulfill({ json: { ok: true, signed_in: true } });
  });
  let accepted = false;
  await page.route("**/api/auth/me", (r) =>
    signedIn
      ? r.fulfill({
          json: {
            user_id: "u-new", username: "new@example.com", role: "user", onboarding_completed: false,
            terms: { current: "2026-10-17", accepted: accepted ? "2026-10-17" : null, accepted_at: null, required: !accepted },
          },
        })
      : r.fulfill({ json: { authenticated: false } }),
  );
  await page.route("**/api/terms/accept", (r) => {
    accepted = true;
    return r.fulfill({ json: { ok: true, terms: { current: "2026-10-17", accepted: "2026-10-17", accepted_at: new Date(e2eNow()).toISOString(), required: false } } });
  });
  await page.goto("/signup?token=T1");
  await page.locator('input[type="password"]').first().fill("Correct-Horse-9-Battery");
  await page.locator('input[type="password"]').nth(1).fill("Correct-Horse-9-Battery");
  await page.getByRole("button", { name: "Create account" }).click();
}

test("a new user sees the terms gate before the Rivian connect step", async ({ page }) => {
  await signupFlow(page);
  const gate = page.locator("[data-terms-gate]");
  await expect(gate).toBeVisible();
  await expect(page.getByPlaceholder("Password")).toHaveCount(0); // no Rivian credentials form yet
  await gate.getByRole("checkbox").check();
  await gate.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByText("Connect your Rivian")).toBeVisible();
  await expect(page.locator("[data-terms-gate]")).toHaveCount(0);
});

test("one Sign in line: the page's own until the confirmation brings its own", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.route("**/api/auth/oidc/", (r) => r.fulfill({ json: [] }));
  await page.route("**/api/signup/request", (r) => r.fulfill({ json: { ok: true, auto_approved: true } }));
  await page.goto("/signup");
  const lines = page.getByText("Already have an account?");
  await expect(lines).toHaveCount(1);
  await page.getByPlaceholder("you@example.com").fill("new@example.com");
  await page.getByRole("button", { name: "Send signup link" }).click();
  await expect(page.getByText("Check your inbox.")).toBeVisible();
  await expect(lines).toHaveCount(1);
  await expect(page.locator("[data-signup-have-account]")).toBeVisible();
});
