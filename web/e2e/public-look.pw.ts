import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// The 13 public pages (no app shell) carry the one look too: html[data-ui="next"] from index.html. Each is
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

// The sample-trip pages read the plan the server puts in the page as JSON (e2e/fixtures, as in sample-trips.pw.ts).
const fixture = (f: string) => fs.readFileSync(path.join(process.cwd(), "e2e", "fixtures", f), "utf8");
const EMBED: Record<string, [string, string]> = {
  "sample-trips": ["sample-trips", fixture("sample-trips-hub.json")],
  "sample-trip": ["sample-trip", fixture("sample-trip.json")],
};

const PAGES: [string, string, { signedIn?: boolean; landing?: boolean }][] = [
  ["landing", "/", { signedIn: false, landing: true }],
  ["login", "/login", { signedIn: false }],
  ["signup", "/signup", { signedIn: false }],
  ["onboarding", "/onboarding", {}],
  ["privacy", "/privacy", { signedIn: false }],
  ["terms", "/terms", { signedIn: false }],
  ["changelog", "/changelog", { signedIn: false }],
  ["sample-trips", "/sample-trips", { signedIn: false }],
  ["sample-trip", "/sample-trips/la-las-vegas-two-days", { signedIn: false }],
  ["lease-calculator", "/lease-calculator", { signedIn: false }],
  ["charging-cost", "/charging-cost", { signedIn: false }],
  ["moving-from-electrafi", "/moving-from-electrafi", { signedIn: false }],
  ["safe-to-connect", "/safe-to-connect", { signedIn: false }],
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
        const embed = EMBED[name];
        if (embed) {
          await page.addInitScript(([id, json]) => {
            document.addEventListener("DOMContentLoaded", () => {
              const el = document.createElement("script");
              el.type = "application/json";
              el.id = id;
              el.textContent = json;
              document.body.appendChild(el);
            });
          }, embed);
        }
        await page.goto(url);
        await expect(page.locator("html")).toHaveAttribute("data-ui", "next");
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
          // The app's compact type scale stays inside the app shells: a public page keeps its own sizes (the lead
          // paragraph, display headings), so every text element is the size it would be without the look's layer.
          const sizes = () =>
            page.evaluate(() =>
              [...document.querySelectorAll("body *")]
                .filter((e) => [...e.childNodes].some((n) => n.nodeType === 3 && n.textContent!.trim()))
                .map((e) => `${getComputedStyle(e).fontSize} ${(e.textContent || "").trim().slice(0, 40)}`),
            );
          const withLayer = await sizes();
          await page.evaluate(() => document.documentElement.removeAttribute("data-ui"));
          const without = await sizes();
          await page.evaluate(() => document.documentElement.setAttribute("data-ui", "next"));
          expect(withLayer).toEqual(without);
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
