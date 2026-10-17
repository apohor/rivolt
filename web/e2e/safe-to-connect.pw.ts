import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// /safe-to-connect: what connecting a Rivian account means (public, signed out), from one markdown
// source, linked from the landing page's login section.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`safe-to-connect page · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await stubApi(page, { signedIn: false });
      await page.goto("/safe-to-connect");
      const doc = page.locator("[data-legal]");
      await expect(doc.getByRole("heading", { name: /Is it safe to connect my Rivian account to Rivolt/, level: 1 })).toBeVisible();
      await expect(doc.getByRole("heading", { name: "How to undo it", level: 2 })).toBeVisible();
      const text = await doc.innerText();
      expect(text).toContain("Authorized Driver");
      expect(text).toContain("password is never stored");
            expect(text).toContain("not affiliated with, endorsed by or sponsored by Rivian");
      await page.waitForTimeout(300);
      await page.screenshot({ path: path.join(SHOTS, `safe-to-connect-${w}-${theme}.png`), fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("the landing's login section links the page", async ({ page }) => {
  await stubApi(page, { signedIn: false, landing: true });
  await page.goto("/");
  await page.getByRole("link", { name: "Is it safe to connect my Rivian account?" }).click();
  await expect(page).toHaveURL(/\/safe-to-connect$/);
  await expect(page.getByRole("heading", { name: /Is it safe to connect my Rivian account to Rivolt/, level: 1 })).toBeVisible();
});
