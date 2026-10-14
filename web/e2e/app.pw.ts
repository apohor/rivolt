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
  { name: "money", path: "/money", heading: "Money", text: ["Statement"] },
  { name: "activity", path: "/activity", heading: "Activity", text: ["short drives"] },
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
