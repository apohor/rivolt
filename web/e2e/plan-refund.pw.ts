import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// Settings → Account → Plan for a web Pro account inside the 14-day refund window: the offer, the confirm step, Keep Pro (changing your
// mind), the refund, and the card afterwards (free tier, with the message). Stubs: GET/POST /api/billing/refund and /api/auth/me.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
const json = (body: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
const me = { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, billing_enabled: true, manage_subscription: true, history_days: 180 };
const pro = { ...me, plan: "pro", plan_source: "stripe", plan_period_end: "2027-11-20T00:00:00Z", plan_period_end_date: "2027-11-20", plan_date_basis: "utc_date" };
const free = { ...me, plan: "free", plan_source: null, manage_subscription: false, plan_ended_at: "2026-11-24T00:00:00Z", plan_ended_source: "stripe", plan_ended_date: "2026-11-24", plan_date_basis: "utc_date" };

for (const v of [
  { n: "393", vp: { width: 393, height: 852 } },
  { n: "1440", vp: { width: 1440, height: 900 } },
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`refund and cancel, ${v.n} ${theme}: offer, change of mind, refund`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await stubApi(page);
      let refunded = false;
      const posts: string[] = [];
      await page.route("**/api/auth/me", (r) => r.fulfill(json(refunded ? free : pro)));
      await page.route("**/api/billing/refund", (r) => {
        if (r.request().method() === "POST") {
          posts.push("POST");
          refunded = true;
          return r.fulfill(json({ refunded: true, amount_cents: 3499, currency: "usd" }));
        }
        return r.fulfill(json(refunded ? { eligible: false, reason: "used" } : { eligible: true, until: "2026-12-05T10:00:00Z", amount_cents: 3499, currency: "usd" }));
      });
      await page.goto("/settings#plan");
      const card = page.locator("[data-plan-card]");
      const block = card.locator("[data-plan-refund]");
      await expect(block).toContainText("$34.99");
      await expect(block).toContainText("free tier");
      await page.screenshot({ path: path.join(SHOTS, `plan-refund-offer-${v.n}-${theme}.png`) });
      // First press only asks; Keep Pro goes back; nothing was sent.
      await block.getByRole("button", { name: "Refund and cancel" }).click();
      await expect(block).toContainText("once per account");
      await page.screenshot({ path: path.join(SHOTS, `plan-refund-confirm-${v.n}-${theme}.png`) });
      await block.getByRole("button", { name: "Keep Pro" }).click();
      await expect(block.getByRole("button", { name: "Refund and cancel" })).toBeVisible();
      expect(posts).toEqual([]);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      // Again, and go through with it.
      await block.getByRole("button", { name: "Refund and cancel" }).click();
      await block.getByRole("button", { name: "Yes, refund and cancel" }).click();
      await expect(card.locator("[data-refund-done]")).toContainText("Refunded $34.99");
      await expect(card).toHaveAttribute("data-plan-card", "lapsed");
      await expect(card.locator("[data-plan-refund]")).toHaveCount(0);
      expect(posts).toEqual(["POST"]);
      await page.screenshot({ path: path.join(SHOTS, `plan-refund-done-${v.n}-${theme}.png`) });
      await ctx.close();
    });
  }
}

test("no refund offer: the card shows no Refund and cancel button", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 393, height: 852 } });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.route("**/api/auth/me", (r) => r.fulfill(json(pro)));
  await page.route("**/api/billing/refund", (r) => r.fulfill(json({ eligible: false, reason: "window_closed" })));
  await page.goto("/settings#plan");
  await expect(page.locator("[data-plan-card='pro']")).toBeVisible();
  await expect(page.getByRole("button", { name: "Refund and cancel" })).toHaveCount(0);
  await ctx.close();
});
