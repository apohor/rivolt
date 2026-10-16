import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// The public demo shell: every /demo/* route renders the real page, and a
// signed-out visitor never trips an endpoint outside the demo allowlist
// (scratchpad/demo-parity-ux-calls.md): no 401s, no console errors.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const ALLOW: RegExp[] = [
  /^\/api\/demo$/, /^\/api\/config$/, /^\/api\/health$/,
  /^\/api\/vehicles$/, /^\/api\/vehicles\/[^/]+\/(now|pack-health|drain)$/,
  /^\/api\/state\/[^/]+$/,
  /^\/api\/ledger\/(enabled|monthly|summary|trips)$/,
  /^\/api\/charges(\/clusters)?$/, /^\/api\/drives$/, /^\/api\/samples$/, /^\/api\/sleep-activity$/,
  /^\/api\/settings\/(charging|charging\/networks|home-location)\/?$/,
  /^\/api\/trips\/(plan|plan-multiday|saved)$/, /^\/api\/trips\/saved\/[^/]+\/actual$/,
  /^\/api\/geocode$/, /^\/api\/maps\//,
];

function watch(page: Page) {
  const bad: string[] = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/") && !ALLOW.some((a) => a.test(u.pathname))) bad.push(`not allowlisted: ${r.method()} ${u.pathname}`);
  });
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname.startsWith("/api/") && r.status() === 401) bad.push(`401: ${u.pathname}`);
  });
  page.on("console", (m) => {
    if (m.type() === "error" && !/tiles|pmtiles|Failed to load resource.*(404|416)/i.test(m.text())) bad.push(`console: ${m.text().slice(0, 140)}`);
  });
  page.on("pageerror", (e) => bad.push(`pageerror: ${e.message.slice(0, 140)}`));
  return bad;
}

const ROUTES = [
  { name: "now", url: "/demo", text: /Sample truck\./ },
  { name: "activity", url: "/demo/activity", text: /Drives and charges/ },
  { name: "money", url: "/demo/money", text: /What charging actually cost/ },
  { name: "plan", url: "/demo/trips/plan", text: /./ },
  { name: "truck", url: "/demo/truck", text: /Pack capacity/ },
  { name: "settings", url: "/demo/settings", text: /./ },
];

for (const r of ROUTES) {
  test(`demo route ${r.name}: renders, no 401s or console errors`, async ({ page }) => {
    await stubApi(page, { demo: true, signedIn: false });
    const bad = watch(page);
    await page.goto(r.url);
    await expect(page.getByText(r.text).first()).toBeVisible();
    // The shell nav is the app's, under /demo.
    await expect(page.getByRole("link", { name: "Activity" }).first()).toHaveAttribute("href", "/demo/activity");
    await page.waitForTimeout(1200);
    expect(bad, bad.join("\n")).toEqual([]);
  });
}

for (const [vpName, vp] of [
  ["iphone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 900 }],
] as const) {
  for (const theme of ["dark", "light"] as const) {
    test(`demo screenshots · ${vpName} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
      await stubApi(page, { demo: true, signedIn: false });
      const bad = watch(page);
      for (const r of ROUTES) {
        await page.goto(r.url);
        await expect(page.getByText(r.text).first()).toBeVisible();
        await page.waitForTimeout(900);
        await page.screenshot({ path: path.join(SHOTS, `demo-${r.name}-${vpName}-${theme}.png`), fullPage: vpName === "desktop" ? false : false });
      }
      expect(bad, bad.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}
