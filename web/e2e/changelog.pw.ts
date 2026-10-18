import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// /changelog: what's new in each release, public and signed out, from GET /api/changelog. Newest
// first, one section per release anchored at its tag (the Atom feed links to #vX.Y.Z), notes as
// Markdown without raw HTML, and the not-affiliated line.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const RELEASES = [
  {
    tag: "v0.36.21",
    published_at: "2026-10-18T20:56:28Z",
    title: "v0.36.21",
    body_md: "## Fixes\n\n- **Admin:** clicking anywhere on a user in the admin Users list opens that user again.\n\n## Behind the scenes\n\n- Dependency updates now merge automatically once they're at least three days old and every check passes.\n\n<img src=x onerror=\"window.__xss=1\">",
    url: "https://github.com/apohor/rivolt/releases/tag/v0.36.21",
  },
  {
    tag: "v0.36.20",
    published_at: "2026-10-18T20:29:34Z",
    title: "v0.36.20",
    body_md: "## What's new\n\n- **Safer sign-in:** after signing in you're always returned to a page on Rivolt itself.\n- **Clearer emails:** links in signup and monthly statement emails always point to rivolt.dev.\n\n**Full Changelog**: https://github.com/apohor/rivolt/compare/v0.36.8...v0.36.9",
    url: "https://github.com/apohor/rivolt/releases/tag/v0.36.20",
  },
];

async function stubChangelog(page: Page, status = 200) {
  await page.route("**/api/changelog", (route) =>
    status === 200
      ? route.fulfill({ json: { releases: RELEASES } })
      : route.fulfill({ status, json: { error: "release notes are unavailable right now" } }),
  );
}

for (const theme of ["dark", "light"] as const) {
  for (const w of [390, 1280] as const) {
    test(`changelog page · ${w}px · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await stubApi(page, { signedIn: false });
      await stubChangelog(page);
      await page.goto("/changelog");
      const doc = page.locator("[data-changelog]");
      await expect(doc.getByRole("heading", { name: "What's new in Rivolt", level: 1 })).toBeVisible();
      const ids = await doc.locator("section").evaluateAll((els) => els.map((e) => e.id));
      expect(ids).toEqual(["v0.36.21", "v0.36.20"]);
      await expect(doc.locator("#v0\\.36\\.21").getByRole("heading", { level: 3, name: "Fixes" })).toBeVisible();
      await expect(doc.locator("#v0\\.36\\.21")).toContainText("October 18, 2026");
      // Raw HTML in the notes is never rendered.
      await expect(doc.locator("img")).toHaveCount(0);
      await expect(doc).not.toContainText("onerror");
      expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined();
      await expect(page.getByText("Rivolt is independent and not affiliated with Rivian.")).toBeVisible();
      await expect(page.getByRole("link", { name: "Feed", exact: true })).toHaveAttribute("href", "/changelog.atom");
      await page.waitForTimeout(300);
      await page.screenshot({ path: path.join(SHOTS, `changelog-${w}-${theme}.png`), fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
      const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
      const summary = ax.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("a feed link lands on its release", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await stubChangelog(page);
  await page.goto("/changelog#v0.36.20");
  await expect(page.locator("#v0\\.36\\.20")).toBeInViewport();
});

test("unavailable notes say so; linked from the signed-in footer", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await stubChangelog(page, 503);
  await page.goto("/changelog");
  await expect(page.getByText(/Release notes are unavailable right now/)).toBeVisible({ timeout: 10_000 });

  await stubApi(page, { signedIn: true });
  await stubChangelog(page);
  await page.goto("/");
  await page.locator("footer").getByRole("link", { name: "What's new" }).click();
  await expect(page).toHaveURL(/\/changelog$/);
  await expect(page.getByRole("heading", { name: "What's new in Rivolt" })).toBeVisible();
});

test("the feed is advertised in the page head", async ({ page }) => {
  await stubApi(page, { signedIn: false });
  await stubChangelog(page);
  await page.goto("/changelog");
  await expect(page.locator('link[rel="alternate"][type="application/atom+xml"]')).toHaveAttribute("href", "/changelog.atom");
});
