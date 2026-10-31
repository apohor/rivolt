import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// Plan polish: result hero (cost, arrival arc, time), route
// timeline, compact form. Screenshots and axe.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    for (const trip of [
      { key: "stop", re: /LA to Las Vegas/, wait: /Sample Fast Charge, Barstow/ },
      { key: "nostop", re: /LA to San Diego/, wait: null },
    ] as const) {
      {
        const state = "on";
        test(`plan ${state}: ${trip.key} · ${v.name} · ${theme}`, async ({ browser }) => {
          const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
          const page = await ctx.newPage();
          await page.addInitScript((t) => localStorage.setItem("rivolt.theme", t), theme);
          await stubApi(page, { demo: true, signedIn: false });
          await page.goto("/demo/trips/plan");
          await page.getByTestId("demo-presets").getByRole("button", { name: trip.re }).click();
          if (trip.wait) await expect(page.getByText(trip.wait).first()).toBeVisible();
          await page.waitForTimeout(1000);
          const timeline = page.getByRole("list", { name: "Route and charging stops" });
          await expect(timeline).toBeVisible();
          await expect(page.locator("[data-plan-hero]")).toBeVisible();
          await page.screenshot({ path: path.join(SHOTS, `plan-${trip.key}-${v.name}-${theme}-${state}.png`), fullPage: v.name === "iphone" });
          const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container, .maplibregl-map").analyze();
          const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
          expect(summary, summary.join("\n")).toEqual([]);
          await ctx.close();
        });
      }
    }
  }
}

{
  const state = "on";
  test(`signed-in form ${state}: route bar: slider and swap, mode chips under More options, only in the new look`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await page.addInitScript(() => localStorage.setItem("rivolt.theme", "dark"));
    await stubApi(page);
    await page.goto("/trips/plan");
    await expect(page.getByRole("button", { name: "Plan trip" })).toBeVisible();
    const n = 1;
    await expect(page.getByRole("slider", { name: "Starting SoC" })).toHaveCount(n);
    await expect(page.getByRole("button", { name: /Swap From and To/ })).toHaveCount(n);
    // The mode chips live under "More options" (collapsed until opened) in the new look.
    await expect(page.getByRole("radiogroup", { name: "Drive mode" })).toHaveCount(0);
    await page.getByRole("button", { name: /More options/ }).click();
    await expect(page.getByRole("radiogroup", { name: "Drive mode" })).toHaveCount(1);
    await expect(page.getByRole("button", { name: /More options/ })).toHaveAttribute("aria-expanded", "true");
    await page.waitForTimeout(600);
    await page.screenshot({ path: path.join(SHOTS, `plan-form-iphone-dark-${state}.png`), fullPage: true });
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container, .maplibregl-map").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n2) => n2.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    test(`stop card expands and collapses · ${v.name} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { demo: true, signedIn: false });
      await page.goto("/demo/trips/plan");
      await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      if (v.name === "iphone") {
        // On a phone the itinerary is in the bottom sheet, inert at its peek
        // (by design): open the sheet before touching a stop.
        const sheet = page.locator("[data-plan-sheet]");
        await expect(sheet).toHaveAttribute("data-snap", "peek");
        // The handle must be what a finger hits at its centre, not the summary above it.
        const handleHit = await page.evaluate(() => {
          const h = document.querySelector("[data-plan-sheet-handle]")!;
          const r = h.getBoundingClientRect();
          const el = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
          return el === h;
        });
        expect(handleHit).toBe(true);
        await page.getByRole("button", { name: "Expand Trip details" }).click();
        await expect(sheet).toHaveAttribute("data-snap", "half");
      }
      const stop = page.getByRole("button", { name: /Sample Fast Charge, Barstow/ });
      await expect(stop).toBeVisible();
      await expect(stop).toHaveAttribute("aria-expanded", "false");
      await expect(page.locator("[data-plan-stop-detail]")).toHaveCount(0);
      await stop.click();
      await expect(stop).toHaveAttribute("aria-expanded", "true");
      const detail = page.locator("[data-plan-stop-detail]");
      await expect(detail).toBeVisible();
      if (v.name === "iphone") {
        // The card reveals itself: its title row ends up inside the sheet body
        // without any scrolling by the test.
        await expect
          .poll(async () => {
            const t = await stop.boundingBox();
            const b = await page.locator("[data-plan-sheet-body]").boundingBox();
            return !!t && !!b && t.y >= b.y - 1 && t.y + t.height <= b.y + b.height + 1;
          })
          .toBe(true);
      }
      await expect(detail).toContainText("Charge time");
      await expect(detail).toContainText("Max power");
      await detail.scrollIntoViewIfNeeded();
      await page.waitForTimeout(500);
      await page.screenshot({ path: path.join(SHOTS, `plan-stopcard-${v.name}-${theme}.png`) });
      const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container, .maplibregl-map").analyze();
      const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await stop.click();
      await expect(detail).toHaveCount(0);
      await ctx.close();
    });
  }
}
