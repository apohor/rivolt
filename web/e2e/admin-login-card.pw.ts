import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// /login on the admin site is a plain card: no product pitch, no sign-up, no links to pages the admin
// site does not serve. On the public site it is unchanged.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");

async function open(page: Page, adminOnly: boolean, delayConfigMs = 0) {
  await stubApi(page, { signedIn: false });
  await page.route("**/api/config", async (r) => {
    if (delayConfigMs) await new Promise((res) => setTimeout(res, delayConfigMs));
    return r.fulfill({ json: { features: { trip_planner_enabled: true, signup_enabled: true, ...(adminOnly ? { admin_only: true } : {}) }, tiles: { url: "", chargers_url: "" }, valhalla: { path: "" }, impersonation: { disabled: true } } });
  });
  await page.route("**/api/auth/oidc/", (r) => r.fulfill({ json: [{ name: "google", display_name: "Google", start_url: "/api/auth/oidc/google/start" }] }));
  await page.goto("/login");
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`admin login card · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 800 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await open(page, true);
      await expect(page.getByRole("heading", { name: "Sign in to Rivolt Admin", level: 1 })).toBeVisible();
      await expect(page.getByRole("button", { name: /Continue with Google/ })).toBeVisible();
      for (const gone of [/Beta/, /Your Rivian companion/, /Live telemetry/, /Road-trip planner/, /Create an account/, /New here\?/]) {
        await expect(page.getByText(gone), String(gone)).toHaveCount(0);
      }
      await expect(page.getByRole("link", { name: /Privacy|Terms/ })).toHaveCount(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `admin-login-card-${w}-${theme}.png`) });
      await ctx.close();
    });
  }
}

test("the consumer copy never flashes up on the admin site while the config loads", async ({ page }) => {
  await open(page, true, 700);
  // Config still in flight: the plain card, not the pitch.
  await expect(page.getByRole("heading", { name: /Sign in/, level: 1 })).toBeVisible();
  await expect(page.getByText(/Your Rivian companion/)).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Sign in to Rivolt Admin" })).toBeVisible({ timeout: 5000 });
  await page.waitForTimeout(300);
  await expect(page.getByText(/Your Rivian companion/)).toHaveCount(0);
});

test("the public site never shows the admin heading, even while the config loads", async ({ page }) => {
  await open(page, false, 700);
  // In flight: a neutral heading, no pitch, and never the admin wording.
  await expect(page.getByRole("heading", { name: "Sign in", exact: true, level: 1 })).toBeVisible();
  await expect(page.getByText("Sign in to Rivolt Admin")).toHaveCount(0);
  await expect(page.getByText(/Your Rivian companion/)).toBeVisible({ timeout: 5000 }); // the config answered
  await expect(page.getByText("Sign in to Rivolt Admin")).toHaveCount(0);
});

test("the public site's login is unchanged", async ({ page }) => {
  await open(page, false);
  await expect(page.getByRole("heading", { name: "Sign in", exact: true, level: 1 })).toBeVisible();
  await expect(page.getByText(/Your Rivian companion/)).toBeVisible();
  await expect(page.getByText("Beta")).toBeVisible();
  await expect(page.getByRole("link", { name: "Create an account" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Privacy policy" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Terms" })).toBeVisible();
});
