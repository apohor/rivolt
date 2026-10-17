import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// /privacy: the short policy, public and signed out, from one markdown source. Placeholders the
// owner fills in stay visible; the draft markers, the banner and the Mail line are gone.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`privacy page · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await stubApi(page, { signedIn: false });
      await page.goto("/privacy");
      const doc = page.locator("[data-privacy]");
      await expect(doc.getByRole("heading", { name: "Rivolt Privacy Policy", level: 1 })).toBeVisible();
      await expect(doc).toContainText("What we collect, and why");
      await expect(doc).toContainText("We do not sell your data");
      // The operator and publication date are filled in; placeholders, draft furniture and
      // the unpublished Terms are gone.
      await expect(doc).toContainText("Anton Pohorilyi");
      await expect(doc).toContainText("Texas");
      await expect(doc).toContainText("Last updated: October 17, 2026");
      const text = await doc.innerText();
      expect(text.match(/\[[A-Z][A-Z ]+\]/g)).toBeNull();
      for (const gone of ["DRAFT", "ATTORNEY TO CONFIRM", "[VERIFY", "[CONFIRM", "Mail:", "MAILING ADDRESS", "Internal notes", "[OWNER LEGAL NAME]", "[STATE]", "Terms"]) expect(text, gone).not.toContain(gone);
      await expect(doc.getByRole("link", { name: /terms/i })).toHaveCount(0);
      await expect(doc.getByText("anton@rivolt.dev").first()).toBeVisible();
      await page.waitForTimeout(300);
      await page.screenshot({ path: path.join(SHOTS, `privacy-${w}-${theme}.png`), fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("linked from the footer, and from the sign-in screen; works on the demo host too", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await page.goto("/login");
  await page.getByRole("link", { name: "Privacy policy" }).click();
  await expect(page).toHaveURL(/\/privacy$/);
  await expect(page.getByRole("heading", { name: "Rivolt Privacy Policy" })).toBeVisible();
  // The signed-in footer links to it too.
  await stubApi(page, { signedIn: true });
  await page.goto("/");
  await page.locator("footer").getByRole("link", { name: "Privacy" }).click();
  await expect(page).toHaveURL(/\/privacy$/);
});

test("demo-only deployment: /privacy is not bounced to /demo", async ({ page }) => {
  await stubApi(page, { demo: true, demoOnly: true, signedIn: false });
  await page.goto("/privacy");
  await expect(page).toHaveURL(/\/privacy$/);
  await expect(page.getByRole("heading", { name: "Rivolt Privacy Policy" })).toBeVisible();
});
