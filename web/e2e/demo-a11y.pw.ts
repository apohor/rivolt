import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// axe (WCAG 2.0/2.1 A + AA) on every /demo route, iPhone and desktop, dark
// and light. The page must be clean; the tile canvas is out of scope.
const TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "best-practice"];

type Step = { name: string; url: string; ready: (p: Page) => Promise<void> };
const STEPS: Step[] = [
  { name: "now", url: "/demo", ready: async (p) => { await expect(p.getByText("Battery").first()).toBeVisible(); } },
  { name: "activity", url: "/demo/activity", ready: async (p) => { await expect(p.getByRole("heading", { name: "Activity" })).toBeVisible(); } },
  { name: "money", url: "/demo/money", ready: async (p) => { await expect(p.getByRole("region", { name: "Statement" })).toBeVisible(); } },
  { name: "plan", url: "/demo/trips/plan", ready: async (p) => { await expect(p.getByTestId("demo-presets")).toBeVisible(); } },
  {
    name: "plan-preset",
    url: "/demo/trips/plan",
    ready: async (p) => {
      await p.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      await expect(p.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
    },
  },
  { name: "truck", url: "/demo/truck", ready: async (p) => { await expect(p.getByText("Battery capacity")).toBeVisible(); } },
  { name: "settings", url: "/demo/settings", ready: async (p) => { await expect(p.getByRole("heading", { name: "Appearance" })).toBeVisible(); } },
];

for (const [vpName, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const scheme of ["dark", "light"] as const) {
    for (const st of STEPS) {
      test(`a11y ${st.name} · ${vpName} · ${scheme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: vp, colorScheme: scheme });
        const page = await ctx.newPage();
        await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), scheme);
        await stubApi(page, { demo: true, signedIn: false });
        await page.goto(st.url);
        await st.ready(page);
        await page.waitForTimeout(800);
        const res = await new AxeBuilder({ page }).withTags(TAGS).exclude(".leaflet-container").analyze();
        const summary = res.violations.map((v) => `${v.id} (${v.impact}): ${v.nodes.length} × ${v.help} :: ${v.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
        expect(summary, summary.join("\n")).toEqual([]);
        await ctx.close();
      });
    }
  }
}

// Keyboard: Tab order reaches the notice CTA, then the nav, and every
// focused control has an accessible name and a visible focus indicator.
for (const url of ["/demo", "/demo/trips/plan", "/demo/settings"]) {
  test(`keyboard focus is visible and named on ${url}`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await stubApi(page, { demo: true, signedIn: false });
    await page.goto(url);
    await page.waitForTimeout(1000);
    const problems: string[] = [];
    const seen: string[] = [];
    for (let i = 0; i < 40; i++) {
      await page.keyboard.press("Tab");
      const info = await page.evaluate(() => {
        const el = document.activeElement as HTMLElement | null;
        if (!el || el === document.body) return null;
        const cs = getComputedStyle(el);
        const ring = (parseFloat(cs.outlineWidth) > 0 && cs.outlineStyle !== "none") || (cs.boxShadow && cs.boxShadow !== "none");
        const name = (el.getAttribute("aria-label") || el.textContent || el.getAttribute("title") || "").trim().slice(0, 40);
        return { tag: el.tagName.toLowerCase(), name, ring: !!ring, href: el.getAttribute("href") };
      });
      if (!info) continue;
      seen.push(`${info.tag}:${info.name}`);
      if (!info.name) problems.push(`no name: ${info.tag} ${info.href ?? ""}`);
      if (!info.ring) problems.push(`no visible focus: ${info.tag} "${info.name}"`);
    }
    // The first stops are the notice CTA, then the app nav.
    expect(seen[0]).toMatch(/Connect with a driver account|Rivolt demo/);
    expect(seen.some((s) => /Activity/.test(s))).toBe(true);
    expect(problems, problems.join("\n")).toEqual([]);
  });
}
