import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The 13 public pages (no app shell) carry the one look too, with their own type sizes. Each is
// axe-clean at phone and desktop width in both themes, and the legal and changelog deep links still land
// on their heading.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

// Enough releases that an older one has to be scrolled to.
const RELEASES = Array.from({ length: 8 }, (_, i) => ({
  tag: `v0.30.${8 - i}`,
  published_at: `2026-10-${String(10 + i).padStart(2, "0")}T12:00:00Z`,
  title: `v0.30.${8 - i}`,
  body_md: "## Fixes\n\n- One fix, written for people.\n- Another, a little longer so the section wraps on a phone.\n\n## Behind the scenes\n\n- Dependency updates.",
  url: `https://github.com/apohor/rivolt/releases/tag/v0.30.${8 - i}`,
}));
const stubChangelog = (page: Page) => page.route("**/api/changelog", (r) => r.fulfill({ json: { releases: RELEASES } }));

const PAGES: [string, string, { signedIn?: boolean; landing?: boolean }][] = [
  ["landing", "/", { signedIn: false, landing: true }],
  ["login", "/login", { signedIn: false }],
  ["signup", "/signup", { signedIn: false }],
  ["onboarding", "/onboarding", {}],
  ["privacy", "/privacy", { signedIn: false }],
  ["terms", "/terms", { signedIn: false }],
  ["changelog", "/changelog", { signedIn: false }],
  ["lease-calculator", "/lease-calculator", { signedIn: false }],
  ["charging-cost", "/charging-cost", { signedIn: false }],
  ["moving-from-electrafi", "/moving-from-electrafi", { signedIn: false }],
  ["safe-to-connect", "/safe-to-connect", { signedIn: false }],
  ["support", "/support", { signedIn: false }],
];

for (const [name, url, opts] of PAGES) {
  for (const w of [390, 1280] as const) {
    for (const theme of ["dark", "light"] as const) {
      test(`public look: ${name} · ${w}px · ${theme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: { width: w, height: 900 }, colorScheme: theme });
        const page = await ctx.newPage();
        await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
        await stubApi(page, opts);
        await stubChangelog(page);
        await page.goto(url);
        await expect(page.getByRole("heading").first()).toBeVisible();
        await expect(page.getByText(/^loading/i)).toHaveCount(0);
        await expect(page.getByText(/didn't load/)).toHaveCount(0);
        expect(await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)).toBe(false);
        const ax = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
        const bad = ax.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
        const summary = bad.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
        expect(summary, summary.join("\n")).toEqual([]);
        await page.screenshot({ path: path.join(SHOTS, `public-${name}-${w}-${theme}.png`), fullPage: true });
        if (w === 1280 && theme === "dark") {
          // The app's compact type scale (next-type.css) applies only inside an app shell's <main data-app-main>.
          // A public page has none, and each Tailwind size class keeps Tailwind's size (the lead paragraph, the
          // display headings): at 1280 px every sm/md/lg/xl variant applies, so the widest one is expected.
          const off = await page.evaluate(() => {
            const px: Record<string, number> = { xs: 12, sm: 14, base: 16, lg: 18, xl: 20, "2xl": 24, "3xl": 30, "4xl": 36, "5xl": 48, "6xl": 60 };
            const order = ["", "sm:", "md:", "lg:", "xl:"];
            const bad: string[] = [];
            if (document.querySelector("main[data-app-main]")) bad.push("an app-shell <main> on a public page");
            for (const el of document.querySelectorAll<HTMLElement>("body [class*='text-']")) {
              if (el.closest(".ui-label")) continue;
              let want: number | undefined;
              let rank = -1;
              for (const c of el.classList) {
                const m = /^((?:sm|md|lg|xl):)?text-(xs|sm|base|lg|xl|2xl|3xl|4xl|5xl|6xl)$/.exec(c);
                if (!m) continue;
                const r = order.indexOf(m[1] ?? "");
                if (r > rank) { rank = r; want = px[m[2]]; }
              }
              const got = parseFloat(getComputedStyle(el).fontSize);
              if (want !== undefined && got !== want) bad.push(`${el.tagName.toLowerCase()}.${[...el.classList].join(".")}: ${got}px, want ${want}px`);
            }
            return bad;
          });
          expect(off, off.join("\n")).toEqual([]);
        }
        await ctx.close();
      });
    }
  }
}

// Deep links: a numbered terms section (id="2") and an older changelog release (id="vX.Y.Z", where the Atom
// feed links) still scroll their heading to the top under the larger type.
for (const [url, sel] of [["/terms#2", "h2[id='2']"], ["/changelog#v0.30.3", "section[id='v0.30.3']"]] as const) {
  test(`public look: deep link lands on its heading · ${url}`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await stubApi(page, { signedIn: false });
    await stubChangelog(page);
    await page.goto(url);
    const el = page.locator(sel);
    await expect(el).toBeVisible();
    await expect.poll(async () => Math.round((await el.boundingBox())!.y)).toBeLessThanOrEqual(40);
    expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(200);
  });
}
