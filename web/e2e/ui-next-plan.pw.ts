import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi } from "./stub";

// ui_theme_next on Plan (the demo's preset planner stands in for a signed-in
// plan: same page): before/after, flag-off check, axe.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const VIEWS = [
  { name: "iphone", vp: { width: 390, height: 844 } },
  { name: "desktop", vp: { width: 1280, height: 900 } },
] as const;

for (const v of VIEWS) {
  for (const state of ["off", "next"] as const) {
    test(`ui ${state}: plan with a preset open · ${v.name} · dark`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
      const page = await ctx.newPage();
      await page.addInitScript((s) => {
        localStorage.setItem("rivolt.ui.next", s === "next" ? "on" : "off");
        localStorage.setItem("rivolt.theme", "dark");
      }, state);
      await stubApi(page, { demo: true, signedIn: false });
      await page.goto("/demo/trips/plan");
      if (state === "off") await expect(page.locator("html")).not.toHaveAttribute("data-ui", "next");
      await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
      await page.waitForTimeout(900);
      await page.screenshot({ path: path.join(SHOTS, `uinext-plan-${v.name}-${state}.png`), fullPage: v.name === "iphone" });
      if (state === "next") {
        const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
        const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
        expect(summary, summary.join("\n")).toEqual([]);
      }
      await ctx.close();
    });
  }
}

test("next-mode on a phone: map first, then the summary and itinerary; Saved and Plan vs actual live in tabs", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo/trips/plan");
  await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  const y = async (sel: string) => (await page.locator(sel).first().boundingBox())!.y;
  const bar = await y("[data-plan-bar]");
  const map = await y("[data-plan-area='map']");
  const summary = await y("[data-plan-area='summary']");
  const itinerary = await y("[data-plan-area='itinerary']");
  expect(bar).toBeLessThan(map);
  expect(map).toBeLessThan(summary);
  expect(summary).toBeLessThan(itinerary);
  // The planner tab is only planning.
  await expect(page.getByRole("heading", { name: "Plan vs actual" })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Saved trips" })).toHaveCount(0);
  await page.getByRole("tab", { name: "Plan vs actual" }).click();
  await expect(page.getByRole("heading", { name: "Plan vs actual" })).toBeVisible();
  await page.getByRole("tab", { name: "Saved" }).click();
  await expect(page.getByRole("heading", { name: "Saved trips" })).toBeVisible();
  await ctx.close();
});

test("flag off: the planner DOM is flat in today's order, with no empty wrapper gap", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "off"));
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo/trips/plan");
  await expect(page.getByTestId("demo-presets")).toBeVisible();
  // No new wrappers when the flag is off (so nothing can add a gap), and the
  // empty result leaves no trailing element.
  await expect(page.locator("[data-ui-area='p-body'], [data-ui-area='p-result'], [data-ui-area='p-below']")).toHaveCount(0);
  await ctx.close();
});
