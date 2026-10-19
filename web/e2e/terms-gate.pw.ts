import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// The "I agree" gate: shown to a signed-in user whose accepted Terms version is not the current one,
// before the app. Unticked checkbox, Continue disabled until ticked, links open new tabs, sign out
// stays available; /privacy and /terms stay reachable; the demo never gates.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

type Terms = { current: string; accepted: string | null; accepted_at: string | null; required: boolean };
async function open(page: Page, terms: Terms, opts: { acceptStatus?: number } = {}) {
  let t = { ...terms };
  const posted: unknown[] = [];
  await stubApi(page, { scenario: "parked" });
  await page.route("**/api/auth/me", (r) =>
    r.fulfill({ json: { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, terms: t } }),
  );
  await page.route("**/api/terms/accept", (r) => {
    posted.push(r.request().postDataJSON());
    if (opts.acceptStatus === 409) {
      t = { current: "2026-11-01", accepted: t.accepted, accepted_at: null, required: true };
      return r.fulfill({ status: 409, json: { error: "version_not_current", current: "2026-11-01" } });
    }
    t = { ...t, accepted: t.current, accepted_at: new Date(e2eNow()).toISOString(), required: false };
    return r.fulfill({ json: { ok: true, terms: t } });
  });
  return posted;
}
const NEW: Terms = { current: "2026-10-17", accepted: null, accepted_at: null, required: true };

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`terms gate · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 800 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await open(page, NEW);
      await page.goto("/");
      const gate = page.locator("[data-terms-gate]");
      await expect(gate).toBeVisible();
      await expect(gate.getByRole("heading", { name: "One more step" })).toBeVisible();
      const box = gate.getByRole("checkbox", { name: "I agree to the Terms of Service and Privacy Policy" });
      await expect(box).not.toBeChecked();
      await expect(gate.getByRole("button", { name: "Continue" })).toBeDisabled();
      for (const [name, href] of [["Terms of Service", "/terms"], ["Privacy Policy", "/privacy"]] as const) {
        const a = gate.getByRole("link", { name });
        await expect(a).toHaveAttribute("href", href);
        await expect(a).toHaveAttribute("target", "_blank");
        await expect(a).toHaveAttribute("rel", "noopener noreferrer");
      }
      await expect(gate.getByRole("button", { name: "Sign out" })).toBeVisible();
      // The app behind it is not reachable.
      await expect(page.getByRole("region", { name: "Now" })).toHaveCount(0);
      await page.waitForTimeout(300);
      await page.screenshot({ path: path.join(SHOTS, `terms-gate-${w}-${theme}.png`) });
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("ticking enables Continue; it posts the current version and lets you into the app", async ({ page }) => {
  const posted = await open(page, NEW);
  await page.goto("/");
  const gate = page.locator("[data-terms-gate]");
  await gate.getByRole("checkbox").check();
  await expect(gate.getByRole("button", { name: "Continue" })).toBeEnabled();
  await gate.getByRole("button", { name: "Continue" }).click();
  await expect(gate).toHaveCount(0);
  await expect(page.getByRole("region", { name: "Now" })).toBeVisible();
  expect(posted).toEqual([{ version: "2026-10-17" }]);
});

test("updated terms say so; a version that moved on shows the new one, unticked", async ({ page }) => {
  await open(page, { current: "2026-10-17", accepted: "2026-01-01", accepted_at: "2026-01-02T00:00:00Z", required: true }, { acceptStatus: 409 });
  await page.goto("/");
  const gate = page.locator("[data-terms-gate]");
  await expect(gate.getByRole("heading", { name: "We updated our terms" })).toBeVisible();
  await gate.getByRole("checkbox").check();
  await gate.getByRole("button", { name: "Continue" }).click();
  // 409: still gated, checkbox cleared, no scary error.
  await expect(gate).toBeVisible();
  await expect(gate.getByRole("checkbox")).not.toBeChecked();
  await expect(gate.getByRole("button", { name: "Continue" })).toBeDisabled();
  await expect(gate.getByRole("alert")).toHaveCount(0);
});

test("up to date: no gate; legal pages and sign-in stay reachable while gated; the demo never gates", async ({ browser }) => {
  let ctx = await browser.newContext();
  let page = await ctx.newPage();
  await open(page, { current: "2026-10-17", accepted: "2026-10-17", accepted_at: "2026-10-17T00:00:00Z", required: false });
  await page.goto("/");
  await expect(page.locator("[data-terms-gate]")).toHaveCount(0);
  await expect(page.getByRole("region", { name: "Now" })).toBeVisible();
  await ctx.close();

  // An existing account that never accepted: terms.required is false, so no gate and no notice.
  ctx = await browser.newContext();
  page = await ctx.newPage();
  await open(page, { current: "2026-10-17", accepted: null, accepted_at: null, required: false });
  await page.goto("/");
  await expect(page.getByRole("region", { name: "Now" })).toBeVisible();
  await expect(page.locator("[data-terms-gate]")).toHaveCount(0);
  await ctx.close();

  ctx = await browser.newContext();
  page = await ctx.newPage();
  await open(page, NEW);
  await page.goto("/privacy");
  await expect(page.getByRole("heading", { name: "Rivolt Privacy Policy" })).toBeVisible();
  await expect(page.locator("[data-terms-gate]")).toHaveCount(0);
  // (/terms is open the same way; its page ships in the /terms PR.)
  await page.goto("/login");
  await expect(page.locator("[data-terms-gate]")).toHaveCount(0);
  await ctx.close();

  ctx = await browser.newContext();
  page = await ctx.newPage();
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo");
  await expect(page.locator("[data-terms-gate]")).toHaveCount(0);
  await ctx.close();
});
