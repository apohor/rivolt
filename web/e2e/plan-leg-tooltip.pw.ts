import { expect, test } from "@playwright/test";
import path from "node:path";
import fs from "node:fs";
import { DEMO_PLAN, stubApi } from "./stub";

// The leg-weather tooltip on a phone: it stays inside the map (its terrain credit wraps instead of running off the right edge), it sits above the
// charging-stop markers (which used to cover "Feels like" and the estimate line), and the first leg is titled "Start to ..." when the origin has no name.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Las Vegas", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
const PLAN = {
  ...DEMO_PLAN,
  // The origin has no name, as a plan from the truck's position does.
  Routes: DEMO_PLAN.Routes.map((r, i) => (i === 0 ? { ...r, Waypoints: r.Waypoints.map((w, k) => (k === 0 ? { ...w, Name: "" } : w)) } : r)),
  weather_adjustment: {
    adjusted_arrival_soc: [34, 18],
    final_arrival_soc: 18,
    below_target: false,
    target_arrival_soc: 10,
    profile_multiplier: 1.03,
    legs: [
      { multiplier: 1.14, temp_c: 3, air_temp_c: 5, headwind_kph: 40, precip_mm: 2, climb_m: 579, descent_m: 100 },
      { multiplier: 1.2, temp_c: -6, headwind_kph: -20, precip_mm: 3, snow: true, climb_m: 220, descent_m: 640 },
    ],
  },
};

for (const v of [
  { n: "iphone-390", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`@webkit the leg tooltip stays inside the map, above the markers · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
    await page.route("**/api/trips/plan", (r) => r.fulfill(j(PLAN)));
    await page.goto("/trips/plan");
    await page.getByRole("tab", { name: "Saved" }).click();
    await page.getByRole("button", { name: /Old trip/ }).click();
    await page.getByRole("tab", { name: "Planner" }).click();
    await page.getByRole("button", { name: "Plan trip" }).click();
    await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();

    // A selected stop's marker is raised (z-index 1000): the case qa hit on rc.2, where that marker painted over the tooltip. Select it first.
    await page.getByText(/Sample Fast Charge, Barstow/).first().dispatchEvent("click");
    await expect.poll(() => page.evaluate(() => [...document.querySelectorAll<HTMLElement>(".maplibregl-marker > div, .maplibregl-marker")].some((e) => e.style.zIndex === "1000"))).toBe(true);
    const badges = page.locator("[data-leg-badge]");
    await expect(badges).toHaveCount(2);
    for (let i = 0; i < 2; i++) {
      // dispatchEvent, not a pointer click: the first popup may sit over the second badge, which a click would wait on for ever.
      await badges.nth(i).dispatchEvent("click");
      const pop = page.locator("[data-leg-popup]");
      await expect(pop).toBeVisible();
      if (i === 0) await expect(pop).toContainText(/^(Start|To) .*(to Sample Fast Charge|Sample Fast Charge)/);
      if (i === 0) await expect(pop).not.toContainText("the stop");
      await expect(pop).toContainText("Terrain: USGS 3DEP");
      await page.waitForTimeout(700); // the map may be nudged to clear the chips and the sheet
      // The whole popup card is inside the map's box, text included (nothing runs off the right edge).
      const geom = await page.evaluate(() => {
        const card = document.querySelector("[data-leg-popup]")!.closest(".maplibregl-popup-content") as HTMLElement;
        const map = document.querySelector(".maplibregl-map") as HTMLElement;
        const c = card.getBoundingClientRect(), m = map.getBoundingClientRect();
        // Every line of text is inside the card: the card's content does not overflow its box.
        const overflowX = card.scrollWidth - card.clientWidth;
        // The topmost element at the centre of each text line must belong to the popup (no marker or badge painted over it).
        const lines = [...document.querySelectorAll("[data-leg-popup] > div")].map((el) => {
          const r = (el as HTMLElement).getBoundingClientRect();
          const top = document.elementFromPoint(Math.min(r.left + r.width / 2, m.right - 2), r.top + r.height / 2);
          return { text: (el as HTMLElement).textContent?.slice(0, 24), covered: !top || !card.contains(top), by: top ? `${top.tagName}.${(top as HTMLElement).className}`.slice(0, 80) : "nothing" };
        });
        return { left: c.left - m.left, right: m.right - c.right, top: c.top - m.top, bottom: m.bottom - c.bottom, overflowX, lines };
      });
      expect(geom.left, JSON.stringify(geom)).toBeGreaterThanOrEqual(-0.5);
      expect(geom.right, JSON.stringify(geom)).toBeGreaterThanOrEqual(-0.5);
      expect(geom.overflowX, JSON.stringify(geom)).toBeLessThanOrEqual(1);
      expect(geom.lines.filter((l) => l.covered), JSON.stringify(geom.lines)).toEqual([]);
      expect(geom.bottom, JSON.stringify(geom)).toBeGreaterThanOrEqual(-0.5);
      // The tooltip is stacked above every marker, the selected stop's raised one included.
      const stack = await page.evaluate(() => {
        const pop = document.querySelector("[data-leg-popup]")!.closest(".maplibregl-popup") as HTMLElement;
        const z = (el: Element) => Number.parseInt(getComputedStyle(el).zIndex) || 0;
        return { popup: z(pop), markers: Math.max(0, ...[...document.querySelectorAll(".maplibregl-marker, .maplibregl-marker *")].map(z)) };
      });
      expect(stack.popup, JSON.stringify(stack)).toBeGreaterThan(stack.markers);
      await page.screenshot({ path: path.join(SHOTS, `leg-tooltip-${v.n}-${i}.png`) });
      await page.evaluate(() => (document.querySelector(".maplibregl-popup-close-button") as HTMLElement | null)?.click());
      await expect(page.locator("[data-leg-popup]")).toHaveCount(0);
    }
    await ctx.close();
  });
}

// The map can move after a tooltip opens (the pan to a selected stop on a slow machine, the visitor dragging): the nightly Monday run saw the first leg's card
// at left -24 px. Open each badge's tooltip, then drag the map so its point goes near the left edge, then near the right edge, and the whole card must stay inside
// the map each time (the popup is re-anchored when the map stops).
for (const v of [
  { n: "iphone-320", vp: { width: 320, height: 700 } },
  { n: "iphone-390", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`the leg tooltip stays inside the map after the map moves · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
    await page.route("**/api/trips/plan", (r) => r.fulfill(j(PLAN)));
    await page.goto("/trips/plan");
    await page.getByRole("tab", { name: "Saved" }).click();
    await page.getByRole("button", { name: /Old trip/ }).click();
    await page.getByRole("tab", { name: "Planner" }).click();
    await page.getByRole("button", { name: "Plan trip" }).click();
    await expect(page.locator("[data-leg-badge]")).toHaveCount(2);
    const popupBox = () =>
      page.evaluate(() => {
        const card = document.querySelector("[data-leg-popup]")!.closest(".maplibregl-popup-content") as HTMLElement;
        const m = (document.querySelector(".maplibregl-map") as HTMLElement).getBoundingClientRect();
        const c = card.getBoundingClientRect();
        return { left: c.left - m.left, right: m.right - c.right, cls: card.closest(".maplibregl-popup")!.className };
      });
    for (let i = 0; i < 2; i++) {
      await page.locator("[data-leg-badge]").nth(i).dispatchEvent("click");
      await expect(page.locator("[data-leg-popup]")).toBeVisible();
      await page.waitForTimeout(700);
      for (const toward of ["left", "right"] as const) {
        // The arrow keys pan the focused map by 100 px a press: walk the badge toward the chosen edge until it is within 70 px of it (at most 6 presses).
        await page.locator(".maplibregl-canvas").first().focus();
        for (let k = 0; k < 6; k++) {
          const m = await page.evaluate((idx) => {
            const mb = (document.querySelector(".maplibregl-map") as HTMLElement).getBoundingClientRect();
            const b = document.querySelectorAll("[data-leg-badge]")[idx].getBoundingClientRect();
            return { w: mb.width, bx: b.left + b.width / 2 - mb.left };
          }, i);
          const toEdge = toward === "left" ? m.bx : m.w - m.bx;
          if (toEdge <= 70 || toEdge - 100 < 12) break; // close enough, or one more press would carry the badge off the map
          await page.keyboard.press(toward === "left" ? "ArrowRight" : "ArrowLeft"); // ArrowRight moves the view right, so the content (and the badge) go left
          await page.waitForTimeout(450);
        }
        await page.waitForTimeout(500);
        if (!(await page.locator("[data-leg-popup]").count())) continue;
        // Only when the popup's own point is on the map (its transform's second translate is that point; the badge slides along the route as the map moves).
        const onMap = await page.evaluate(() => {
          const mb = (document.querySelector(".maplibregl-map") as HTMLElement).getBoundingClientRect();
          const t = (document.querySelector(".maplibregl-popup") as HTMLElement).style.transform;
          const x = Number.parseFloat(/translate\((-?[\d.]+)px,\s*-?[\d.]+px\)\s*$/.exec(t)?.[1] ?? "NaN");
          return x > 32 && x < mb.width - 32; // a corner-anchored card reaches up to ~25 px beyond its point
        });
        if (!onMap) continue;
        const box = await popupBox();
        expect(box.left, JSON.stringify({ i, toward, ...box })).toBeGreaterThanOrEqual(-0.5);
        expect(box.right, JSON.stringify({ i, toward, ...box })).toBeGreaterThanOrEqual(-0.5);
      }
      await page.evaluate(() => (document.querySelector(".maplibregl-popup-close-button") as HTMLElement | null)?.click());
    }
    await ctx.close();
  });
}

// The map can move after the tooltip opens (the pan to a selected stop, the nudge clear of the chips, a drag) and the phone's sheet can change height:
// the card's height follows the room its point has then, so it neither runs out of the map or under the sheet nor scrolls a line away for nothing (#1887).
test("@webkit iphone-390: the leg tooltip refits its height when the map moves under it", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(PLAN)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.locator("[data-leg-badge]")).toHaveCount(2);
  await page.waitForTimeout(700);
  await page.locator("[data-leg-badge]").first().dispatchEvent("click");
  await expect(page.locator("[data-leg-popup]")).toContainText("Terrain: USGS 3DEP");
  await page.waitForTimeout(700);
  const box = () =>
    page.evaluate(() => {
      const r = document.querySelector("[data-leg-popup]") as HTMLElement;
      const c = (r.closest(".maplibregl-popup-content") as HTMLElement).getBoundingClientRect();
      const m = (document.querySelector(".maplibregl-map") as HTMLElement).getBoundingClientRect();
      const sheet = document.querySelector("[data-plan-sheet]")!.getBoundingClientRect().top;
      const above = (r.closest(".maplibregl-popup") as HTMLElement).className.includes("anchor-bottom");
      return { above, top: c.top - m.top, bottom: Math.min(m.bottom, sheet) - c.bottom, scrolls: r.scrollHeight > r.clientHeight + 1 };
    });
  const before = await box();
  // The card opens whole, above its point.
  expect(before, JSON.stringify(before)).toMatchObject({ above: true, scrolls: false });
  // Drag the map up by more than the room above the card: its point (and the card) go up with it. The drag starts on an empty part of the map, under
  // the route, and stops still so there is no glide.
  const drag = Math.round(before.top + 60);
  await page.mouse.move(330, 640);
  await page.mouse.down();
  await page.mouse.move(330, 640 - drag, { steps: 12 });
  await page.waitForTimeout(300);
  await page.mouse.up();
  await page.waitForTimeout(600);
  const after = await box();
  // Still open, still inside the map: shorter, its lines scrolling inside it.
  expect(after.top, JSON.stringify(after)).toBeGreaterThanOrEqual(-0.5);
  expect(after.bottom, JSON.stringify(after)).toBeGreaterThanOrEqual(-0.5);
  // Dragged back, it is whole again.
  await page.mouse.move(330, 640 - drag);
  await page.mouse.down();
  await page.mouse.move(330, 640, { steps: 12 });
  await page.waitForTimeout(300);
  await page.mouse.up();
  await page.waitForTimeout(600);
  const back = await box();
  expect(back, JSON.stringify(back)).toMatchObject({ scrolls: false });
  expect(back.top, JSON.stringify(back)).toBeGreaterThanOrEqual(-0.5);
  await ctx.close();
});
