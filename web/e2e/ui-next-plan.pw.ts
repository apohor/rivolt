import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { settled } from "./settle";
import { stubApi } from "./stub";

// Plan (the demo's preset planner stands in for a signed-in
// plan: same page): screenshots and axe.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const VIEWS = [
  { name: "iphone", vp: { width: 390, height: 844 } },
  { name: "desktop", vp: { width: 1280, height: 900 } },
] as const;

for (const v of VIEWS) {
  {
    const state = "next";
    test(`ui ${state}: plan with a preset open · ${v.name} · dark`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
      const page = await ctx.newPage();
      await page.addInitScript(() => localStorage.setItem("rivolt.theme", "dark"));
      await stubApi(page, { demo: true, signedIn: false });
      await page.goto("/demo/trips/plan");
      await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
      await page.waitForTimeout(900);
      await page.screenshot({ path: path.join(SHOTS, `uinext-plan-${v.name}-${state}.png`), fullPage: v.name === "iphone" });
      const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".maplibregl-map").analyze();
      const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
      expect(summary, summary.join("\n")).toEqual([]);
      await ctx.close();
    });
  }
}

test("next-mode on a phone: map first, then the summary and itinerary; Saved and Plan vs actual live in tabs", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page, { demo: true, signedIn: false });
  await page.goto("/demo/trips/plan");
  await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  // The page smooth-scrolls the map into view as the plan arrives, so the four tops are read in one frame and in
  // page coordinates: separate boundingBox() calls each landed at a different point of the scroll. They are read once the
  // page has stopped moving, and each area has to be there first.
  for (const area of ["map", "summary", "itinerary"]) await expect(page.locator(`[data-plan-area='${area}']`)).toBeAttached();
  await settled(page, ["[data-plan-bar]", "[data-plan-area='map']", "[data-plan-area='summary']", "[data-plan-area='itinerary']"]);
  const [bar, map, summary, itinerary] = await page.evaluate(() =>
    ["[data-plan-bar]", "[data-plan-area='map']", "[data-plan-area='summary']", "[data-plan-area='itinerary']"].map(
      (sel) => document.querySelector(sel)!.getBoundingClientRect().top + window.scrollY,
    ),
  );
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

