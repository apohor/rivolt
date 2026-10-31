import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Visual regression for Now, Activity, Money, Truck and Plan,
// iPhone and desktop, dark and light, 20 baselines in
// ui-next-visual.pw.ts-snapshots/. Data comes from the stub, the clock is
// frozen (browser and the stub's Node side), animations are off, and the map
// is masked, so a diff means the UI changed.
//
// Opt-in: set E2E_VISUAL=1 (`npm run e2e:visual`). Baselines carry the OS in
// their name and are made on macOS; Linux CI has none, so the default
// `playwright test` skips this file. Refresh after an intended UI change with
// `npm run e2e:visual:update` and commit the PNGs.
test.skip(!process.env.E2E_VISUAL, "visual baselines run only with E2E_VISUAL=1");

const FIXED = Date.parse("2026-10-15T18:00:00Z");

// The stub builds timestamps and the current month on the Node side, so freeze
// Date here too (this worker process only; restored after the file).
const RealDate = Date;
test.beforeAll(() => {
  class FixedDate extends RealDate {
    constructor(...args: unknown[]) {
      super(...((args.length ? args : [FIXED]) as [string]));
    }
    static now() {
      return FIXED;
    }
  }
  globalThis.Date = FixedDate as unknown as DateConstructor;
});
test.afterAll(() => {
  globalThis.Date = RealDate;
});

const VIEWS = [
  { name: "iphone", vp: { width: 390, height: 844 } },
  { name: "desktop", vp: { width: 1280, height: 900 } },
] as const;
const SCHEMES = ["dark", "light"] as const;

type Shot = { name: string; url: string; ready: (p: Page) => Promise<void>; demo?: boolean };
const SHOTS: Shot[] = [
  { name: "now", url: "/", ready: (p) => expect(p.getByRole("region", { name: "Now" })).toBeVisible() },
  { name: "activity", url: "/activity", ready: (p) => expect(p.getByRole("heading", { name: "Activity" }).first()).toBeVisible() },
  { name: "money", url: "/money", ready: (p) => expect(p.getByRole("region", { name: "Statement" })).toBeVisible() },
  { name: "truck", url: "/truck", ready: (p) => expect(p.getByText("Battery capacity")).toBeVisible() },
  {
    // The demo's preset planner stands in for a signed-in plan: same page.
    name: "plan",
    url: "/demo/trips/plan",
    demo: true,
    ready: async (p) => {
      await p.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      await expect(p.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
    },
  },
];

for (const s of SHOTS) {
  for (const v of VIEWS) {
    for (const scheme of SCHEMES) {
      test(`next ${s.name} · ${v.name} · ${scheme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: v.vp, colorScheme: scheme, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        await page.clock.setFixedTime(FIXED);
        await page.addInitScript((c) => {
          localStorage.setItem("rivolt.theme", c);
        }, scheme);
        await stubApi(page, s.demo ? { demo: true, signedIn: false } : {});
        await page.goto(s.url);
            await s.ready(page);
        await page.evaluate(() => document.fonts.ready);
        await page.waitForTimeout(900);
        await expect(page).toHaveScreenshot(`${s.name}-${v.name}-${scheme}.png`, {
          fullPage: true,
          animations: "disabled",
          caret: "hide",
          mask: [page.locator(".leaflet-tile-pane, .maplibregl-canvas")],
          maxDiffPixelRatio: 0.002,
        });
        await ctx.close();
      });
    }
  }
}
