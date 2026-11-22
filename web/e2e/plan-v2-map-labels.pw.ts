import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// plan_v2, item 4 and the charge ring. The map carries the charge pills and one marker per charging stop, the same ring the itinerary's rail carries (no
// numbers, no weather badges: the weather is in the itinerary rows). Pointing at or picking either the marker or its row highlights both.
const SHOTS = process.env.PLAN_V2_SHOTS || path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

async function openSaved(page: Page, key: boolean, plan: unknown = DEMO_PLAN) {
  await stubApi(page, key ? { designPreview: ["plan_v2"] } : {});
  const t = DEMO_PRESETS[0];
  await page.route("**/api/trips/saved", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ id: "st-v2", name: t.name, inputs: t.inputs, plan, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }]) }),
  );
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /LA to Las Vegas/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await expect(page.locator("[data-plan-area='map'] .maplibregl-marker").first()).toBeVisible();
}

const MARK = ".maplibregl-marker [data-charge-ring]";
const ROW = "[data-plan-timeline] > li[data-ring]";

for (const theme of ["dark", "light"] as const) {
  for (const [name, w, h] of [["393", 393, 852], ["1440", 1440, 900]] as const) {
    test(`key on: a charge ring per charging stop on the map and on the rail, no numbers · ${name} ${theme}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: theme });
      const page = await ctx.newPage();
      await page.addInitScript(([t]) => localStorage.setItem("rivolt.theme", t), [theme]);
      await openSaved(page, true);
      const rings = await page.locator(MARK).count();
      expect(rings).toBeGreaterThan(0);
      // The map marker says its order and charge; there is no number anywhere on it.
      const labels = await page.locator(".maplibregl-marker[aria-label^='Charging stop']").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")));
      expect(labels).toHaveLength(rings);
      labels.forEach((l, i) => expect(l).toMatch(new RegExp(`^Charging stop ${i + 1} of ${rings}, \\d+ to \\d+ percent$`)));
      for (const t of await page.locator(MARK).allInnerTexts()) expect(t.trim()).toBe("");
      await expect(page.locator("[data-stop-number]")).toHaveCount(0);
      // The rail: one ring per charging row, in place of the node, not announced a second time.
      await expect(page.locator(`${ROW} [data-charge-ring-rail]`)).toHaveCount(rings);
      await expect(page.locator(`${ROW} [data-charge-ring-rail]`).first()).toHaveAttribute("aria-hidden", "true");
      expect(await page.locator(ROW).first().evaluate((li) => getComputedStyle(li, "::before").display)).toBe("none");
      // The colours are the theme's.
      const arc = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--cr-arc").trim());
      expect(arc).toBe(theme === "dark" ? "#f59e0b" : "#b45309");
      await expect(page.locator("[data-leg-badge]")).toHaveCount(0);
      await expect(page.locator("[data-weather-chip]")).toHaveCount(0);
      await page.waitForTimeout(800);
      await page.screenshot({ path: path.join(SHOTS, `ring-${name}-${theme}.png`) });
      await ctx.close();
    });
  }
}

test("key on: the ring follows a theme switch", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, true);
  const arc = () => page.locator(MARK).first().evaluate((el) => getComputedStyle(el.querySelector("circle[stroke-linecap]")!).stroke);
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  const light = await arc();
  await page.evaluate(() => document.documentElement.removeAttribute("data-theme"));
  const dark = await arc();
  expect(light).not.toBe(dark);
});

test("key on: pointing at the marker highlights its row, and pointing at the row highlights its marker", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, true);
  const marks = page.locator(".maplibregl-marker:has([data-charge-ring])");
  const rows = page.locator(ROW);
  expect(await marks.count()).toBeGreaterThan(0);
  const on = (loc: ReturnType<Page["locator"]>) => loc.locator("[data-charge-ring]").first().evaluate((e) => e.hasAttribute("data-on"));
  await expect.poll(() => on(marks.first())).toBe(false);
  // The map is still settling after the fit: hover only once the marker's box has stopped moving.
  let last = "";
  await expect.poll(async () => { const b = JSON.stringify(await marks.first().boundingBox()); const same = b === last; last = b; return same; }, { intervals: [150] }).toBe(true);
  await marks.first().hover();
  await expect.poll(() => on(rows.first())).toBe(true);
  await expect.poll(() => on(marks.first())).toBe(true);
  await page.mouse.move(5, 5);
  await expect.poll(() => on(rows.first())).toBe(false);
  await rows.first().locator("button").hover();
  await expect.poll(() => on(marks.first())).toBe(true);
  await expect.poll(() => on(rows.first())).toBe(true);
  await page.mouse.move(5, 5);
  await expect.poll(() => on(marks.first())).toBe(false);
});

test("key on, phone: tapping the marker highlights its row in the sheet", async ({ page }) => {
  await page.setViewportSize({ width: 393, height: 852 });
  await openSaved(page, true);
  const mark = page.locator(".maplibregl-marker:has([data-charge-ring])").first();
  const box = (await mark.boundingBox())!;
  expect(Math.min(box.width, box.height)).toBeGreaterThanOrEqual(28); // a touch target no smaller than before
  await mark.click();
  await expect.poll(() => page.locator(ROW).first().locator("[data-charge-ring]").evaluate((e) => e.hasAttribute("data-on"))).toBe(true);
  await expect(page.locator(ROW).first()).toHaveAttribute("data-selected", "true");
});

test("key off: bolts on the map, no rings and no numbers", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, false);
  await expect(page.locator("[data-stop-number]")).toHaveCount(0);
  await expect(page.locator("[data-charge-ring]")).toHaveCount(0);
  const dots = await page.locator(".maplibregl-marker .trip-route-dot").allInnerTexts();
  expect(dots.some((t) => t.includes("⚡"))).toBe(true);
});

// Six stops, two of them close together, a full 0 to 100 charge and one that adds nothing: every one is a ring, in order, and the close pair stay two tappable markers.
const base = DEMO_PLAN.Routes[0];
const wp = (i: number, lat: number, lon: number, arr: number, dep: number) => ({ ...base.Waypoints[1], EntityID: `demo-${i}`, Name: `Sample Charge ${i}`, Latitude: lat, Longitude: lon, ArrivalSoC: arr, DepartureSoC: dep, DistanceFromOriginMeters: i * 60_000 });
const SIX = {
  ...DEMO_PLAN,
  Routes: [{ ...base, Waypoints: [base.Waypoints[0], wp(1, 34.3, -117.9, 20, 60), wp(2, 34.9, -117.0, 0, 100), wp(3, 35.0, -116.6, 30, 30), wp(4, 35.04, -116.55, 25, 70), wp(5, 35.6, -115.8, 12, 85), wp(6, 35.9, -115.4, 18, 55), base.Waypoints[2]] }],
};
for (const [name, w, h] of [["393", 393, 852], ["1440", 1440, 900]] as const) {
  test(`key on: six stops, two close together, edge charges · ${name}`, async ({ page }) => {
    await page.setViewportSize({ width: w, height: h });
    await openSaved(page, true, SIX);
    await expect(page.locator(MARK)).toHaveCount(6);
    const labels = await page.locator(".maplibregl-marker[aria-label^='Charging stop']").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")));
    expect(labels.map((l) => l!.split(",")[0])).toEqual([1, 2, 3, 4, 5, 6].map((n) => `Charging stop ${n} of 6`));
    expect(labels[1]).toBe("Charging stop 2 of 6, 0 to 100 percent");
    await expect(page.locator(`${ROW} [data-charge-ring-rail]`)).toHaveCount(6);
    await page.waitForTimeout(800);
    await page.screenshot({ path: path.join(SHOTS, `ring-six-${name}.png`) });
  });
}

// Touch: a tap makes the browser fire compatibility mouse events (mouseover/mouseenter) with no matching leave. With no mouse at all, those events must not
// leave a ring highlighted (hover comes from a mouse pointer only; a tap selects instead).
test("key on, no mouse: compatibility mouse events from a tap leave no ring highlighted", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, hasTouch: true });
  const page = await ctx.newPage();
  await openSaved(page, true);
  const lit = () => page.locator("[data-charge-ring][data-on]").count();
  expect(await lit()).toBe(0);
  await page.locator(ROW).first().evaluate((li) => li.dispatchEvent(new MouseEvent("mouseover", { bubbles: true, relatedTarget: document.body })));
  await page.waitForTimeout(300);
  expect(await lit()).toBe(0);
  await page.locator(".maplibregl-marker:has([data-charge-ring])").first().evaluate((m) => m.dispatchEvent(new MouseEvent("mouseenter")));
  await page.waitForTimeout(300);
  expect(await lit()).toBe(0);
  await ctx.close();
});

test("key on: a charging-stop marker is reachable by Tab and Enter or Space selects its row", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openSaved(page, true);
  const mark = page.locator(".maplibregl-marker[aria-label^='Charging stop']").first();
  expect(await mark.getAttribute("tabindex")).toBe("0");
  await mark.focus();
  await expect(mark).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator(ROW).first()).toHaveAttribute("data-selected", "true");
  await page.locator(ROW).first().locator("button").first().click(); // the row toggles it off
  await expect(page.locator(ROW).first()).not.toHaveAttribute("data-selected", "true");
  await mark.focus();
  await page.keyboard.press("Space");
  await expect(page.locator(ROW).first()).toHaveAttribute("data-selected", "true");
});
