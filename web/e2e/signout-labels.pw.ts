import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");

// Settings > Account has two sign-outs. They are named apart, weighted apart, and signing out of
// Rivian (which deletes the stored session) asks first.
for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`two sign-outs, told apart · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { uiNext: true });
      const posts: string[] = [];
      await page.route("**/api/settings/rivian/logout", (r) => {
        posts.push(r.request().method());
        return r.fulfill({ json: { authenticated: false } });
      });
      await page.goto("/settings?tab=account");
      await expect(page.getByRole("button", { name: "Sign out", exact: true })).toHaveCount(0);
      const rivolt = page.getByRole("button", { name: "Sign out of Rivolt" });
      const rivian = page.getByRole("button", { name: "Sign out of Rivian" });
      await expect(rivolt).toBeVisible();
      await expect(rivian).toBeVisible();
      // Different weight: the Rivolt one is a filled button, the Rivian one an outline.
      const [bgA, bgB] = await Promise.all([
        rivolt.evaluate((e) => getComputedStyle(e).backgroundColor),
        rivian.evaluate((e) => getComputedStyle(e).backgroundColor),
      ]);
      expect(bgA).not.toBe(bgB);
      // A tap alone changes nothing: it asks.
      await rivian.click();
      const confirm = page.locator("[data-rivian-signout-confirm]");
      await expect(confirm).toContainText("Sign out of Rivian?");
      await expect(confirm).toContainText("deletes the stored Rivian session");
      expect(posts).toEqual([]);
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `signout-labels-${w}-${theme}.png`), fullPage: true });
      // Cancel: nothing sent, the button is back.
      await confirm.getByRole("button", { name: "Cancel" }).click();
      await expect(confirm).toHaveCount(0);
      expect(posts).toEqual([]);
      // Confirm: one logout.
      await rivian.click();
      await confirm.getByRole("button", { name: "Yes, sign out of Rivian" }).click();
      await expect.poll(() => posts).toEqual(["POST"]);
      await ctx.close();
    });
  }
}

test("signup: a 429 that is not our JSON says to wait, not 'Error 429'", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.route("**/api/auth/oidc/", (r) => r.fulfill({ json: [] }));
  await page.route("**/api/signup/request", (r) => r.fulfill({ status: 429, contentType: "text/plain", body: "error code: 1015" }));
  await page.goto("/signup");
  await page.getByPlaceholder("you@example.com").fill("new@example.com");
  await page.getByRole("button", { name: "Send signup link" }).click();
  await expect(page.getByRole("alert")).toHaveText("Too many tries. Wait a minute and try again.");
});

test("signup: our own error message still wins on a 429", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.route("**/api/auth/oidc/", (r) => r.fulfill({ json: [] }));
  await page.route("**/api/signup/request", (r) => r.fulfill({ status: 429, json: { error: "slow down a little" } }));
  await page.goto("/signup");
  await page.getByPlaceholder("you@example.com").fill("new@example.com");
  await page.getByRole("button", { name: "Send signup link" }).click();
  await expect(page.getByRole("alert")).toHaveText("slow down a little");
});
