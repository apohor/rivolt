import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// /terms: the Terms of Service, public and signed out, from one markdown source; sections anchored by
// number (/terms#2); linked from the footer, sign-in and the privacy page.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`terms page · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await stubApi(page, { signedIn: false });
      await page.goto("/terms");
      const doc = page.locator("[data-legal]");
      await expect(doc.getByRole("heading", { name: /Rivolt Terms of Service/, level: 1 })).toBeVisible();
      await expect(doc.getByRole("heading", { name: /^2\. Unofficial access to Rivian/ })).toHaveAttribute("id", "2");
      await expect(doc).toContainText("Anton Pohorilyi");
      const text = await doc.innerText();
      // Only [PUBLICATION DATE] remains (Last updated, and the section 11 date), set at release.
      expect(text.match(/\[[A-Z][A-Z ]+\]/g)).toEqual(["[PUBLICATION DATE]", "[PUBLICATION DATE]"]);
      for (const gone of ["DRAFT", "[DECIDE", "[VERIFY", "[ATTORNEY", "[CONFIRM", "Not legal advice"]) expect(text, gone).not.toContain(gone);
      await page.waitForTimeout(300);
      await page.screenshot({ path: path.join(SHOTS, `terms-${w}-${theme}.png`), fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("/terms#2 lands on section 2; the privacy page links there", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.goto("/terms#2");
  const h = page.getByRole("heading", { name: /^2\. Unofficial access to Rivian/ });
  await expect(h).toBeVisible();
  await expect.poll(async () => (await h.boundingBox())!.y).toBeLessThan(120);
  await page.goto("/privacy");
  await page.locator("[data-legal]").getByRole("link", { name: "Terms", exact: true }).click();
  await expect(page).toHaveURL(/\/terms#2$/);
  await expect(page.getByRole("heading", { name: /^2\. Unofficial access to Rivian/ })).toBeVisible();
});

test("linked from the footer and the sign-in screen; works on the demo host", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.goto("/login");
  await page.locator("[data-legal]").getByRole("link", { name: "Terms", exact: true }).click();
  await expect(page).toHaveURL(/\/terms$/);
  await stubApi(page, { signedIn: true });
  await page.goto("/");
  await page.locator("footer").getByRole("link", { name: "Terms" }).click();
  await expect(page).toHaveURL(/\/terms$/);
  await stubApi(page, { demo: true, demoOnly: true, signedIn: false });
  await page.goto("/terms");
  await expect(page).toHaveURL(/\/terms$/);
});
