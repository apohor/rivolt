import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import path from "node:path";
import { stubApi } from "./stub";

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");

// Settings → Notifications: the welcome and setup-reminder emails toggle (on by default, separate
// from the monthly statement).
for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`onboarding email toggle · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await stubApi(page);
      let enabled = true;
      const puts: unknown[] = [];
      await page.route("**/api/settings/onboarding-email", async (r) => {
        if (r.request().method() === "PUT") {
          const body = r.request().postDataJSON();
          puts.push(body);
          enabled = body.enabled;
        }
        return r.fulfill({ json: { enabled } });
      });
      await page.goto("/settings?tab=notifications");
      const sw = page.getByRole("switch", { name: "Welcome and setup reminder emails" });
      await expect(sw).toBeVisible();
      await expect(sw).toHaveAttribute("aria-checked", "true");
      await expect(page.getByText(/up to two setup reminders until your truck is connected/)).toBeVisible();
      await sw.click();
      await expect(sw).toHaveAttribute("aria-checked", "false");
      expect(puts).toEqual([{ enabled: false }]);
      await page.reload();
      await expect(page.getByRole("switch", { name: "Welcome and setup reminder emails" })).toHaveAttribute("aria-checked", "false");
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container, .maplibregl-map").analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await page.screenshot({ path: path.join(SHOTS, `onboarding-email-${w}-${theme}.png`) });
      await ctx.close();
    });
  }
}

test("hidden when the endpoint is absent", async ({ page }) => {
  await stubApi(page);
  await page.route("**/api/settings/onboarding-email", (r) => r.fulfill({ status: 404, json: { error: "not found" } }));
  await page.goto("/settings?tab=notifications");
  await expect(page.locator("#statement-email-toggle")).toBeVisible();
  await expect(page.locator("#onboarding-email")).toHaveCount(0);
});

test("a failed save is shown and the toggle keeps its old state", async ({ page }) => {
  await stubApi(page);
  await page.route("**/api/settings/onboarding-email", (r) =>
    r.request().method() === "PUT" ? r.fulfill({ status: 403, json: { error: "forbidden" } }) : r.fulfill({ json: { enabled: true } }),
  );
  await page.goto("/settings?tab=notifications");
  const sw = page.getByRole("switch", { name: "Welcome and setup reminder emails" });
  await sw.click();
  await expect(page.getByRole("alert").filter({ hasText: "Couldn't save" })).toBeVisible();
  await expect(sw).toHaveAttribute("aria-checked", "true");
});
