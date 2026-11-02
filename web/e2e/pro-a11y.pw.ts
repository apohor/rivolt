import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The Pro surfaces through a keyboard, a screen reader's tree, zoom and reduced motion (pm's accessibility pass): the Plan card in each state,
// the Pro sheet, the lapse and payment-failed notices, the limit notes and the locked vehicle. The server's answers are stubbed.
const json = (body: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(body) });
const BASE = { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, billing_enabled: true, history_days: 90, plan_trial_ends: null };
const FREE = { ...BASE, plan: "free", plan_source: null, plan_period_end: null, plan_cancels_at: null, plan_grace_ends: null, plan_ended_at: null, plan_ended_source: null };
const STRIPE = { ...BASE, plan: "pro", plan_source: "stripe", plan_period_end: "2026-12-01T00:00:00Z", plan_cancels_at: null, plan_grace_ends: null, plan_ended_at: null, plan_ended_source: null };
const CANCELLED = { ...STRIPE, plan_cancels_at: "2026-12-01T00:00:00Z" };
const GRACE = { ...STRIPE, plan_grace_ends: "2026-12-08T00:00:00Z" };
const APPLE = { ...STRIPE, plan_source: "apple" };
const LAPSED = { ...FREE, plan_ended_at: "2026-10-01T00:00:00Z", plan_ended_source: "stripe" };

async function as(page: Page, me: Record<string, unknown>) {
  await stubApi(page);
  await page.route("**/api/auth/me", (r) => r.fulfill(json(me)));
}

// Every rule at A and AA, any impact (the older specs only fail serious and critical).
async function axeClean(page: Page, scope?: string) {
  let b = new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]);
  if (scope) b = b.include(scope);
  const res = await b.analyze();
  expect(res.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(" | ")}`)).toEqual([]);
}

for (const scheme of ["dark", "light"] as const) {
  for (const v of [
    { n: "iphone", vp: { width: 390, height: 844 } },
    { n: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    for (const [state, me, button] of [
      ["free", FREE, "See Pro"],
      ["subscribed (Stripe)", STRIPE, "Manage subscription"],
      ["cancelled", CANCELLED, "Resubscribe"],
      ["payment failed", GRACE, "Update card"],
      ["subscribed (Apple)", APPLE, "Manage subscription"],
    ] as const) {
      test(`Pro: the Plan card and sheet, ${state} · ${v.n} ${scheme}`, async ({ browser }) => {
        test.setTimeout(90_000);
        const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        await as(page, me);
        await page.goto("/settings#plan");
        const card = page.locator("[data-plan-card]");
        await expect(card).toBeVisible();
        await axeClean(page, "[data-plan-card]");
        // The sheet is opened from the keyboard, takes focus, keeps it, and gives it back.
        const opener = card.getByRole("button", { name: button });
        await opener.focus();
        await page.keyboard.press("Enter");
        const sheet = page.locator("[data-pro-sheet]");
        await expect(sheet).toBeVisible();
        await expect(sheet.getByRole("button", { name: "Close" })).toBeFocused();
        for (let i = 0; i < 25; i++) {
          await page.keyboard.press("Tab");
          expect(await sheet.evaluate((el) => el.contains(document.activeElement)), `Tab ${i + 1} left the sheet`).toBe(true);
        }
        for (let i = 0; i < 25; i++) {
          await page.keyboard.press("Shift+Tab");
          expect(await sheet.evaluate((el) => el.contains(document.activeElement)), `Shift+Tab ${i + 1} left the sheet`).toBe(true);
        }
        await axeClean(page, "[data-pro-sheet]");
        // Nothing moves: no running animation or transition on the page with reduced motion on.
        expect(await page.evaluate(() => document.getAnimations().filter((a) => a.playState === "running").length)).toBe(0);
        await page.keyboard.press("Escape");
        await expect(sheet).toHaveCount(0);
        await expect(opener).toBeFocused();
        await ctx.close();
      });
    }

    test(`Pro: a choice of plan shows where the keyboard is · ${v.n} ${scheme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await as(page, FREE);
      await page.goto("/settings#plan");
      await page.getByRole("button", { name: "See Pro" }).first().click();
      const sheet = page.locator("[data-pro-sheet]");
      const yearly = sheet.getByRole("radio", { name: /Yearly/ });
      await yearly.focus();
      // The radio is visually hidden: its label (the whole choice) carries a ring while it has focus.
      const ring = await yearly.evaluate((el) => {
        const l = el.closest("label")!;
        const c = getComputedStyle(l);
        return `${c.boxShadow} ${c.outlineStyle}`;
      });
      expect(ring, "the focused plan choice has a visible ring").toMatch(/rgb|solid/);
      await page.keyboard.press("Space");
      await expect(yearly).toBeChecked();
      await expect(sheet.getByRole("checkbox")).not.toBeChecked();
      await axeClean(page, "[data-pro-sheet]");
      await ctx.close();
    });

    test(`Pro: the lapse and payment-failed notices, the limit note and the locked vehicle · ${v.n} ${scheme}`, async ({ browser }) => {
      test.setTimeout(90_000);
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme });
      const page = await ctx.newPage();
      await as(page, LAPSED);
      await page.goto("/");
      await expect(page.locator("[data-plan-notice]")).toBeVisible();
      await axeClean(page, "[data-plan-notice]");
      await page.route("**/api/drives/old-drive", (r) => r.fulfill(json({ error: "outside_free_window", free_days: 90 }, 402)));
      await page.goto("/drives/old-drive");
      const note = page.locator("[data-pro-limit=outside_free_window]");
      await expect(note).toBeVisible();
      await axeClean(page, "[data-pro-limit]");
      await note.getByRole("button", { name: "See Pro" }).focus();
      await page.keyboard.press("Enter");
      await expect(page.locator("[data-pro-sheet]")).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(note.getByRole("button", { name: "See Pro" })).toBeFocused();
      await page.unroute("**/api/auth/me");
      await page.route("**/api/auth/me", (r) => r.fulfill(json(GRACE)));
      await page.goto("/");
      await expect(page.locator("[data-plan-notice=grace]")).toBeVisible();
      await axeClean(page, "[data-plan-notice]");
      await ctx.close();
    });
  }
}

// 320 px wide (a 400% zoom of a 1280 px window) and text at 200%: nothing runs off the side, and the sheet still scrolls to its button.
test("Pro: the sheet at 320 px and at 200% text still reaches its button", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 320, height: 568 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await as(page, FREE);
  await page.goto("/settings#plan");
  await page.addStyleTag({ content: "html { font-size: 200% !important; }" });
  await page.getByRole("button", { name: "See Pro" }).first().click();
  const sheet = page.locator("[data-pro-sheet]");
  await expect(sheet).toBeVisible();
  await sheet.getByText("Yearly").click();
  await sheet.getByRole("checkbox").check();
  const subscribe = sheet.getByRole("button", { name: /Subscribe:/ });
  await subscribe.scrollIntoViewIfNeeded();
  await expect(subscribe).toBeInViewport();
  expect(await sheet.evaluate((el) => el.scrollWidth <= el.clientWidth), "the sheet itself fits").toBe(true);
  // The page behind: any element wider than the window is reported (and fails the check unless it is outside the Pro surfaces).
  const wide = await page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>("body *")]
      .filter((el) => el.getBoundingClientRect().right > window.innerWidth + 1 && !el.closest("[data-pro-sheet]") && getComputedStyle(el).position !== "fixed")
      .slice(0, 8)
      .map((el) => `${el.tagName.toLowerCase()}${el.id ? "#" + el.id : ""}.${String(el.className).split(" ").slice(0, 3).join(".")} right=${Math.round(el.getBoundingClientRect().right)}`),
  );
  expect(wide, "elements wider than the window at 320 px and 200% text").toEqual([]);
  await ctx.close();
});
