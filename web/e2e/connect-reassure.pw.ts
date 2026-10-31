import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import path from "node:path";
import { stubApi } from "./stub";

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");

// The connect step answers "why hand over my Rivian login?" above the form, in Settings and in
// Onboarding, and the existing privacy line stays.
for (const route of ["/settings?tab=account", "/onboarding"] as const) {
  for (const theme of ["dark", "light"] as const) {
    for (const w of [390, 1280] as const) {
      test(`connect reassurance · ${route} · ${w}px · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript(([t]) => {
          localStorage.setItem("rivolt.theme", t);
        }, [theme]);
        await stubApi(page, { rivian: "disconnected" });
        await page.goto(route);
        const box = page.locator("[data-connect-reassure]");
        await expect(box).toBeVisible();
        await expect(box).toContainText("separate, free Authorized Driver account");
        await expect(box).toContainText("Your password is never stored");
        await expect(box).toContainText("can't control your truck");
        await expect(box).toContainText("remove the driver in the Rivian app");
        const guide = box.getByRole("link", { name: "How to set one up" });
        await expect(guide).toHaveAttribute("href", /docs\/SIGNUP\.md#recommended-connect-a-dedicated-authorized-driver-account$/);
        await expect(guide).toHaveAttribute("rel", "noopener noreferrer");
        await expect(page.locator("[data-connect-privacy]")).toBeVisible();
        const [bb, sb] = await Promise.all([box.boundingBox(), page.getByRole("button", { name: /^Sign in/ }).first().boundingBox()]);
        expect(bb!.y + bb!.height).toBeLessThanOrEqual(sb!.y);
        expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
        const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".maplibregl-map").analyze();
        const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
        expect(summary, summary.join("\n")).toEqual([]);
        await page.screenshot({ path: path.join(SHOTS, `connect-reassure-${route.includes("onb") ? "onb" : "set"}-${w}-${theme}.png`) });
        await ctx.close();
      });
    }
  }
}
