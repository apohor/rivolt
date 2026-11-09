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
