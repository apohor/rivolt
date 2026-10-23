import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { stubApi, type NowScenario, type StubOptions } from "./stub";
import { e2eNow } from "./clock";

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
  // Render in a zone where it is midday now (see middayZone).
  midday?: boolean;
};

// middayZone is a fixed-offset zone where the local time is about noon right
// now. The stub's drives sit minutes to an hour before now, so in the browser's
// own zone, just after midnight, they split across two days and a same-day
// grouping (the Activity errands row) can't form: the test would depend on the
// time it runs.
function middayZone(): string {
  const off = Math.max(-12, Math.min(14, 12 - new Date(e2eNow()).getUTCHours()));
  // Etc/GMT signs are inverted: Etc/GMT-5 is UTC+5.
  return off === 0 ? "Etc/GMT" : `Etc/GMT${off > 0 ? "-" : "+"}${Math.abs(off)}`;
}

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
  { name: "now-endpoint", path: "/", heading: "Now", opts: { scenario: "parked", nowEndpoint: true }, text: ["Replay", /last GPS fix .*before it parked/, /since /] },
  { name: "demo", path: "/demo", heading: /Rivolt demo/, opts: { signedIn: false, demo: true, nowEndpoint: true }, text: ["Sample truck.", "Connect with a driver account"] },
  // Stale frame: the pill must not say Live next to an old timestamp.
  { name: "now-stale", path: "/", heading: "Now", opts: { scenario: "stale" }, text: [/Last seen [5-7] min ago/, /Updated [5-7] min ago/] },
  { name: "demo-stale", path: "/demo", heading: /Rivolt demo/, opts: { signedIn: false, demo: true, scenario: "stale" }, text: [/Last seen [5-7] min ago/] },
  {
    name: "plan-vs-actual",
    // Plan vs actual is a tab of the planner.
    path: "/trips/plan?tab=pva",
    heading: "Trip planner",
    text: [
      "We'll compare once you've driven it.",
      "Confirm",
      "Not this trip",
      "Trip in progress. We'll compare when you're home.",
      /Driven .* · 612 mi/,
      "Fast-charging stops",
      /so far .*2 not priced yet/,
      "Sample network billed $0.48/kWh vs $0.56 planned",
    ],
  },
  { name: "money", path: "/money", heading: "Money", text: ["Statement"] },
  { name: "activity", path: "/activity", heading: "Activity", text: ["short drives"], midday: true },
  // Degraded-state banner: shown only when something is wrong.
  { name: "banner-mfa", path: "/money", heading: "Money", opts: { rivian: "mfa" }, text: ["Rivian is waiting for your verification code."] },
  { name: "banner-reauth", path: "/money", heading: "Money", opts: { rivian: "reauth" }, text: ["Your Rivian sign-in expired. Reconnect to keep recording."] },
  { name: "banner-disconnected", path: "/money", heading: "Money", opts: { rivian: "disconnected" }, text: ["Rivian isn't connected."] },
  { name: "banner-paused", path: "/money", heading: "Money", opts: { rivian: "paused" }, text: ["Rivolt has paused its connection to Rivian."] },
  { name: "banner-backend-down", path: "/money", heading: "Money", opts: { rivian: "backend-down" }, text: ["Rivolt can't reach its server."] },
  { name: "settings", path: "/settings", heading: /Settings/, text: ["Rivolt account"] },
  { name: "truck-drain", path: "/truck", heading: "Truck", opts: { drain: "ok" }, text: ["Parked drain", /% \/ day/, /in the Sleep card below/] },
  { name: "truck-drain-thin", path: "/truck", heading: "Truck", opts: { drain: "insufficient" }, text: ["Not enough parked time yet to measure drain."] },
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
        const ctx = await browser.newContext({ viewport, colorScheme: scheme, timezoneId: view.midday ? middayZone() : undefined });
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
          // The Now card leads the page: no card in main starts above it.
          const top = await page.getByRole("region", { name: "Now" }).evaluate((el) => el.getBoundingClientRect().top);
          const above = await page.locator("main section").evaluateAll((els, t) => els.filter((e) => e.getBoundingClientRect().height > 0 && e.getBoundingClientRect().top < t - 1).length, top);
          expect(above).toBe(0);
        }
        if (!view.name.startsWith("banner-") && view.name !== "landing" && view.name !== "admin-idp") {
          // Healthy: no degraded banner.
          await expect(page.getByRole("status").filter({ hasText: /Rivian|reach its server/ })).toHaveCount(0);
        }
        if (view.name === "truck") {
          // Endpoint absent (404): no drain card at all.
          await expect(page.getByText("Parked drain")).toHaveCount(0);
        }
        if (view.name === "settings") {
          // Sign out lives in Settings now.
          const card = page.locator("section").filter({ has: page.getByRole("heading", { name: "Rivolt account" }) });
          await expect(card.getByRole("button", { name: "Sign out of Rivolt", exact: true })).toHaveCount(1);
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
        if (scheme === "light" && view.name !== "landing") {
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
          const lum = ([r, g, b]: number[]) => 0.2126 * r + 0.7152 * g + 0.0722 * b;
          const hero = page.getByRole("region", { name: "Statement" }).locator("div.text-4xl").first();
          // Computed colours can be oklch(), lab() or color(srgb …), not just
          // rgb(): the browser's own canvas converts any of them to sRGB. The
          // page colour is the first opaque one up from body (else the
          // browser's canvas for the color-scheme), and a translucent text
          // colour is blended over it.
          const [fg, bg] = await hero.evaluate((el) => {
            const ctx = document.createElement("canvas").getContext("2d", { willReadFrequently: true })!;
            const srgb = (c: string) => {
              ctx.clearRect(0, 0, 1, 1);
              ctx.fillStyle = "#000";
              ctx.fillStyle = c;
              ctx.fillRect(0, 0, 1, 1);
              const [r, g, b, a] = ctx.getImageData(0, 0, 1, 1).data;
              return [r, g, b, a / 255];
            };
            let page = getComputedStyle(document.documentElement).colorScheme.includes("dark") ? [18, 18, 18] : [255, 255, 255];
            for (const n of [document.body, document.documentElement]) {
              const c = srgb(getComputedStyle(n).backgroundColor);
              if (c[3] > 0) {
                page = c.slice(0, 3);
                break;
              }
            }
            const t = srgb(getComputedStyle(el).color);
            const text = [0, 1, 2].map((i) => t[i] * t[3] + page[i] * (1 - t[3]));
            return [text, page];
          });
          expect([...fg, ...bg].every(Number.isFinite), `colours ${fg} on ${bg}`).toBe(true);
          expect(Math.abs(lum(fg) - lum(bg))).toBeGreaterThan(80);
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
    await page.getByRole("link", { name: tab }).first().click();
    await page.waitForTimeout(400);
  }
  expect(paths).not.toContain("/api/auth/me");
  expect(paths).not.toContain("/api/settings/rivian/");
  await ctx.close();
});

test("Settings > Notifications: statement email toggle and preview", async ({ page }) => {
  await stubApi(page);
  await page.goto("/settings?tab=notifications");
  const sw = page.getByRole("switch", { name: /statement/i }).or(page.locator("#statement-email-toggle"));
  await expect(sw).toBeVisible();
  await expect(sw).toHaveAttribute("aria-checked", "false");
  await page.getByRole("button", { name: /Preview last month/ }).click();
  await expect(page.getByText(/Your Rivian cost for September/)).toBeVisible();
  await expect(page.getByText(/monthly statements are off for your account/)).toBeVisible();
});

test("statement email deep links: share=1 prompt and unpriced filter", async ({ page }) => {
  await stubApi(page);
  await page.goto("/money?src=statement&share=1");
  await expect(page.getByText("Your statement is ready to share. Tap Share statement.")).toBeVisible();
  await page.goto("/charges?unpriced=1&src=statement");
  await expect(page.getByText("Showing charges that still need a price.")).toBeVisible();
});
