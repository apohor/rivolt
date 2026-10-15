import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, type NowScenario, type StubOptions } from "./stub";

// Smoke + screenshot harness. Every page is stubbed with fake data,
// the money_tab_enabled shell is on, and each view is captured on a
// phone and a desktop viewport in dark and light. PNGs go to
// e2e/__screenshots__ (git-ignored).

const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
const VIEWPORTS = {
  phone: { width: 390, height: 844 },
  desktop: { width: 1280, height: 800 },
} as const;
const SCHEMES = ["dark", "light"] as const;

type View = {
  name: string;
  path: string;
  heading: RegExp | string;
  opts?: StubOptions;
  // Extra text that must be visible.
  text?: (string | RegExp)[];
};

const nowViews: View[] = (["parked", "charging", "driving", "asleep", "offline"] as NowScenario[]).map((s) => ({
  name: `now-${s}`,
  path: "/",
  heading: "Now",
  opts: { scenario: s },
  text: {
    parked: ["Parked at home"],
    charging: ["Charging at home"],
    driving: ["Driving"],
    asleep: ["Asleep"],
    offline: ["Not heard from recently"],
  }[s],
}));

const views: View[] = [
  ...nowViews,
  // With GET /now: the Replay badge, "location as of" and "since".
  { name: "now-endpoint", path: "/", heading: "Now", opts: { scenario: "parked", nowEndpoint: true }, text: ["Replay", /location as of/, /since /] },
  { name: "demo", path: "/demo", heading: /Rivolt demo/, opts: { signedIn: false, demo: true, nowEndpoint: true }, text: ["Sample truck.", "Connect my Rivian"] },
  // Stale frame: the pill must not say Live next to an old timestamp.
  { name: "now-stale", path: "/", heading: "Now", opts: { scenario: "stale" }, text: [/Last seen [5-7] min ago/, /Updated [5-7] min ago/] },
  { name: "demo-stale", path: "/demo", heading: /Rivolt demo/, opts: { signedIn: false, demo: true, scenario: "stale" }, text: [/Last seen [5-7] min ago/] },
  { name: "money", path: "/money", heading: "Money", text: ["Statement"] },
  { name: "activity", path: "/activity", heading: "Activity", text: ["short drives"] },
  // Degraded-state banner: shown only when something is wrong.
  { name: "banner-mfa", path: "/money", heading: "Money", opts: { rivian: "mfa" }, text: ["Rivian is waiting for your verification code."] },
  { name: "banner-reauth", path: "/money", heading: "Money", opts: { rivian: "reauth" }, text: ["Your Rivian sign-in expired. Reconnect to keep recording."] },
  { name: "banner-disconnected", path: "/money", heading: "Money", opts: { rivian: "disconnected" }, text: ["Rivian isn't connected."] },
  { name: "banner-backend-down", path: "/money", heading: "Money", opts: { rivian: "backend-down" }, text: ["Rivolt can't reach its server."] },
  { name: "settings", path: "/settings", heading: /Settings/, text: ["Rivolt account"] },
  { name: "truck", path: "/truck", heading: "Truck", text: ["Pack capacity", "Lowest tire"] },
  { name: "landing", path: "/", heading: /What your Rivian actually costs/, opts: { signedIn: false, landing: true } },
  { name: "admin-idp", path: "/admin#users", heading: /Sign-in removal failures/, text: ["Delete that account instead"] },
];

function collectErrors(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    const t = m.text();
    // Unstubbed endpoints answer 404 on purpose.
    if (/Failed to load resource/.test(t)) return;
    errors.push(`console: ${t}`);
  });
  return errors;
}

test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const view of views) {
  for (const [vpName, viewport] of Object.entries(VIEWPORTS)) {
    for (const scheme of SCHEMES) {
      test(`${view.name} · ${vpName} · ${scheme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport, colorScheme: scheme });
        const page = await ctx.newPage();
        const errors = collectErrors(page);
        await stubApi(page, view.opts);
        await page.goto(view.path);

        // Key heading visible (the Now card is a labelled region).
        if (view.name.startsWith("now-")) {
          await expect(page.getByRole("region", { name: "Now" })).toBeVisible();
        } else {
          await expect(page.getByRole("heading", { name: view.heading }).first()).toBeVisible();
        }
        for (const t of view.text ?? []) {
          await expect(page.getByText(t).first()).toBeVisible();
        }

        // Shell cleanup (U14): no marketing hero, no connection pill and
        // no Sign out in the header, no Overview/Live sub-tabs, and the
        // Now card leads the page.
        if (view.name !== "landing" && view.name !== "admin-idp") {
          await expect(page.getByText("There's a story")).toHaveCount(0);
          await expect(page.getByText("checking…")).toHaveCount(0);
          await expect(page.locator("header").getByRole("button", { name: "Sign out" })).toHaveCount(0);
        }
        if (view.name.startsWith("now-")) {
          await expect(page.getByRole("link", { name: "Overview" })).toHaveCount(0);
          await expect(page.getByRole("link", { name: "Full telemetry →" })).toBeVisible();
          const top = await page.getByRole("region", { name: "Now" }).evaluate((el) => el.getBoundingClientRect().top);
          expect(top).toBeLessThan(200);
        }
        if (!view.name.startsWith("banner-") && view.name !== "landing" && view.name !== "admin-idp") {
          // Healthy: no degraded banner.
          await expect(page.getByRole("status").filter({ hasText: /Rivian|reach its server/ })).toHaveCount(0);
        }
        if (view.name === "settings") {
          // Sign out lives in Settings now.
          const card = page.locator("section,div").filter({ has: page.getByRole("heading", { name: "Rivolt account" }) }).last();
          await expect(card.getByRole("button", { name: "Sign out", exact: true })).toHaveCount(1);
        }
        if (view.name.endsWith("stale")) {
          await expect(page.getByTestId("now-pill")).not.toHaveText("Live");
        }
        if (view.name === "demo" || view.name === "demo-stale") {
          // No sign-in-only links in the public demo.
          await expect(page.getByRole("link", { name: "Full telemetry →" })).toHaveCount(0);
          await expect(page.locator('a[href="/live"]')).toHaveCount(0);
        }
        if (view.name === "activity") {
          // One nav row: the filter tabs; no Feed/Drives/Charges sub-nav.
          await expect(page.getByRole("navigation", { name: "Activity sections" })).toHaveCount(0);
          await expect(page.getByRole("tab")).toHaveCount(3);
        }

        // Light theme only applies inside the shell, signed in.
        if (scheme === "light" && view.name !== "landing" && !view.name.startsWith("demo")) {
          await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
        } else if (scheme === "dark") {
          await expect(page.locator("html")).not.toHaveAttribute("data-theme", "light");
        }

        // No inline scripts anywhere (the server CSP forbids them).
        const inline = await page.evaluate(() => document.querySelectorAll("script:not([src])").length);
        expect(inline).toBe(0);

        // No horizontal scroll on the phone viewport.
        if (vpName === "phone") {
          const overflow = await page.evaluate(
            () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
          );
          expect(overflow).toBeLessThanOrEqual(1);
        }

        // The statement's hero total must contrast with the page (it was
        // once near-white on white in light mode).
        if (view.name === "money") {
          const rgb = (c: string) => (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number);
          const lum = ([r, g, b]: number[]) => 0.2126 * r + 0.7152 * g + 0.0722 * b;
          const hero = page.getByRole("region", { name: "Statement" }).locator("div.text-4xl").first();
          const [fg, bg] = await Promise.all([
            hero.evaluate((el) => getComputedStyle(el).color),
            page.evaluate(() => getComputedStyle(document.body).backgroundColor),
          ]);
          expect(Math.abs(lum(rgb(fg)) - lum(rgb(bg)))).toBeGreaterThan(80);
        }

        await page.waitForTimeout(250);
        await page.screenshot({
          path: path.join(SHOTS, `${scheme}-${vpName}-${view.name}.png`),
          fullPage: true,
        });
        expect(errors).toEqual([]);
        await ctx.close();
      });
    }
  }
}

test("the public demo makes no session or Rivian-account calls", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  const paths: string[] = [];
  page.on("request", (r) => paths.push(new URL(r.url()).pathname));
  await stubApi(page, { signedIn: false, demo: true, nowEndpoint: true });
  await page.goto("/demo");
  await expect(page.getByRole("heading", { name: /Rivolt demo/ })).toBeVisible();
  for (const tab of ["Money", "Activity", "Now"]) {
    await page.getByRole("button", { name: tab }).click();
    await page.waitForTimeout(400);
  }
  expect(paths).not.toContain("/api/auth/me");
  expect(paths).not.toContain("/api/settings/rivian/");
  await ctx.close();
});
