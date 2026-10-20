import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// ui_theme_next planner typography: one family (Geist sans, no mono) and one size
// scale (24/36 headings, 14 values, 12 sub-lines, 11 captions, as on Now/Truck), weights 400-600.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const SIZES = new Set([11, 12, 14, 24, 36]);
const WEIGHTS = new Set([400, 500, 600]);

async function audit(page: Page) {
  return page.evaluate(() => {
    const root = document.querySelector("[data-plan-next]")!;
    const bad: string[] = [];
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    const seen = new Set<Element>();
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (!(n.textContent ?? "").trim()) continue;
      const el = n.parentElement!;
      if (seen.has(el)) continue;
      seen.add(el);
      if (el.closest(".leaflet-container, .leaflet-control-container, .leaflet-pane, script, style")) continue;
      const cs = getComputedStyle(el);
      const size = Math.round(parseFloat(cs.fontSize) * 100) / 100;
      const w = Number(cs.fontWeight);
      const label = `${el.tagName.toLowerCase()}.${String(el.className).slice(0, 40)} "${(n.textContent ?? "").trim().slice(0, 24)}"`;
      if (!/Geist UI/.test(cs.fontFamily) || /mono/i.test(cs.fontFamily)) bad.push(`family ${cs.fontFamily.slice(0, 30)} · ${label}`);
      if (![11, 12, 14, 24, 36].includes(size)) bad.push(`size ${size} · ${label}`);
      if (![400, 500, 600].includes(w)) bad.push(`weight ${w} · ${label}`);
    }
    root.querySelectorAll("input, select, textarea").forEach((el) => {
      const cs = getComputedStyle(el);
      const size = parseFloat(cs.fontSize);
      if (!/Geist UI/.test(cs.fontFamily)) bad.push(`family ${cs.fontFamily.slice(0, 30)} · ${el.tagName} ${(el as HTMLInputElement).name || (el as HTMLInputElement).ariaLabel || ""}`);
      // Inputs stay at 16px so iOS Safari doesn't zoom on focus.
      if (![11, 12, 14, 16, 24, 36].includes(Math.round(size))) bad.push(`size ${size} · ${el.tagName}`);
    });
    return bad;
  });
}
void SIZES;
void WEIGHTS;

const alt = { ...DEMO_PLAN.Routes[0], TotalTripDurationSec: 16_800, ArrivalSoC: 31 };
const plan = { ...DEMO_PLAN, Routes: [DEMO_PLAN.Routes[0], alt], costs: [DEMO_PLAN.costs[0], { ...DEMO_PLAN.costs[0], dcfc_spend_low: 17.9, dcfc_spend_high: 17.9 }] };

for (const theme of ["dark", "light"] as const) {
  for (const v of [
    { name: "iphone", vp: { width: 390, height: 844 } },
    { name: "desktop", vp: { width: 1280, height: 900 } },
  ] as const) {
    test(`planner type scale · demo result, tabs · ${v.name} · ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => {
        localStorage.setItem("rivolt.ui.next", "on");
        localStorage.setItem("rivolt.theme", t);
      }, [theme]);
      await stubApi(page, { demo: true, signedIn: false });
      await page.route(/\/api\/demo\/trips\/la-vegas\/plan$/, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(plan) }));
      await page.goto("/demo/trips/plan");
      await page.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
      // On a phone the itinerary sits in the bottom sheet, inert at its peek: open it first.
      if (v.name === "iphone") {
        await page.getByRole("button", { name: "Expand Trip details" }).click();
        await expect(page.locator("[data-plan-sheet]")).toHaveAttribute("data-snap", "half");
      }
      await page.getByRole("button", { name: /Sample Fast Charge, Barstow/ }).click();
      await page.waitForTimeout(600);
      await page.screenshot({ path: path.join(SHOTS, `plan-type-${v.name}-${theme}-after.png`), fullPage: v.name === "iphone" });
      expect(await audit(page), "planner tab").toEqual([]);
      await page.getByRole("tab", { name: "Saved" }).click();
      await page.waitForTimeout(300);
      expect(await audit(page), "saved tab").toEqual([]);
      await page.getByRole("tab", { name: "Plan vs actual" }).click();
      await page.waitForTimeout(500);
      expect(await audit(page), "plan vs actual tab").toEqual([]);
      await ctx.close();
    });
  }
}

test("planner type scale · signed-in form", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await page.addInitScript(() => localStorage.setItem("rivolt.ui.next", "on"));
  await stubApi(page);
  await page.goto("/trips/plan");
  await expect(page.getByRole("button", { name: "Plan trip" })).toBeVisible();
  await page.waitForTimeout(500);
  expect(await audit(page)).toEqual([]);
  await ctx.close();
});
