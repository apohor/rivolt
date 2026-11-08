import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// One callout at a time (a right click next to a marker used to open the marker's callout and the dropped pin's together), and a callout opened in
// the upper part of the map opens below its point, so it never runs under the layer chips (whose translucent panel made its text unreadable).
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Chicago", 41.88, -87.63), destination: pt("Denver", 39.74, -104.99), extra_stops: [], overnight_flags: [] };

// The longest real what-if answer: every line the card can show, so the callout grows as much as it ever does after it opens.
const LONG_WHAT_IF = {
  estimate: true,
  estimated: true,
  notice: "estimate_replan",
  segment: { from: 0, to: 1 },
  added_m: 48000,
  added_sec: 2400,
  arrival_shift_sec: 2400,
  destination: { arrival_at: "2026-10-18T03:40:00Z" },
  next_charge: { index: 1, is_destination: false, soc_drop_pts: 9 },
  warnings: ["may_not_reach_next_charge", "may_not_reach_destination"],
};

async function planned(page: Page) {
  await stubApi(page);
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(DEMO_PLAN)));
  await page.route("**/api/trips/what-if", (r) => r.fulfill(j(LONG_WHAT_IF)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  // The page scrolls the map into view after a plan; aim only once it has held still.
  let last = -1;
  let still = 0;
  await expect
    .poll(async () => {
      const y = await page.evaluate(() => window.scrollY);
      still = y === last ? still + 1 : 0;
      last = y;
      return still;
    }, { intervals: [100] })
    .toBeGreaterThanOrEqual(3);
  // The map's first full load can still reframe it (fit) after the markers show: aim only once a marker has held still too.
  const marker = page.locator("[data-plan-area='map'] .maplibregl-marker").first();
  await expect(marker).toBeVisible();
  let lastPos = "";
  let stillPos = 0;
  await expect
    .poll(async () => {
      const b = await marker.boundingBox();
      const pos = b ? `${Math.round(b.x)},${Math.round(b.y)}` : "";
      stillPos = pos === lastPos ? stillPos + 1 : 0;
      lastPos = pos;
      return stillPos;
    }, { intervals: [100] })
    .toBeGreaterThanOrEqual(8);
}

// A right click can land before the map has taken its first frame after the page's own scroll and fit; click again until the callout is there.
async function rightClickUntilCallout(page: Page, x: number, y: number) {
  await expect(async () => {
    await page.mouse.click(x, y, { button: "right" });
    await expect(page.getByRole("button", { name: "Add as a stop" })).toBeVisible({ timeout: 1500 });
  }).toPass({ timeout: 10_000 });
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`@webkit iphone: a right click beside a marker leaves one callout, the dropped pin's · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await planned(page);
    const map = page.locator("[data-plan-area='map'] .maplibregl-map").first();
    const marker = map.locator(".maplibregl-marker").nth(1);
    await expect(marker).toBeVisible();
    await marker.click();
    await expect(page.locator(".maplibregl-popup")).toHaveCount(1);
    // Right-click a spot on the map that the open callout does not cover (and the chips do not), near the marker.
    const mb = (await map.boundingBox())!;
    const pop = (await page.locator(".maplibregl-popup").boundingBox())!;
    const chips = (await page.locator("[data-map-chips]").boundingBox())!;
    const inside = (b: { x: number; y: number; width: number; height: number }, x: number, y: number) => x >= b.x - 8 && x <= b.x + b.width + 8 && y >= b.y - 8 && y <= b.y + b.height + 8;
    let spot: [number, number] | undefined;
    for (const [fx, fy] of [[0.5, 0.75], [0.2, 0.6], [0.8, 0.6], [0.5, 0.45], [0.3, 0.85]]) {
      const x = mb.x + mb.width * fx;
      const y = mb.y + mb.height * fy;
      if (!inside(pop, x, y) && !inside(chips, x, y)) { spot = [x, y]; break; }
    }
    expect(spot, "a free spot on the map").toBeTruthy();
    await rightClickUntilCallout(page, spot![0], spot![1]);
    await expect(page.locator(".maplibregl-popup")).toHaveCount(1);
    await ctx.close();
  });

  // The button is what a tap at its centre reaches (not a chip), and the popup does not run up under the chip panel, once the what-if text has landed.
  for (const [band, frac] of [["just under the chip row", 0], ["in the middle of the map", 0.5], ["just above the phone's bottom sheet", -1]] as const) {
    test(`@webkit iphone: a pin dropped ${band} keeps Add as a stop tappable after its text lands · ${v.n}`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
      const page = await ctx.newPage();
      await planned(page);
      const map = page.locator("[data-plan-area='map'] .maplibregl-map").first();
      await expect(map.locator(".maplibregl-marker").first()).toBeVisible();
      // The chip row grows when its "no chargers here" note appears after the map's first load: aim only once its height has held still.
      let lastH = -1;
      let stillH = 0;
      await expect
        .poll(async () => {
          const h = (await page.locator("[data-map-chips]").boundingBox())!.height;
          stillH = h === lastH ? stillH + 1 : 0;
          lastH = h;
          return stillH;
        }, { intervals: [100] })
        .toBeGreaterThanOrEqual(5);
      const row = (await page.locator("[data-map-chips]").boundingBox())!;
      const mb = (await map.boundingBox())!;
      const sheetEl = page.locator("[data-plan-sheet]");
      const sheet = (await sheetEl.count()) ? await sheetEl.first().boundingBox() : null;
      test.skip(frac < 0 && !sheet, "no bottom sheet at this width");
      const clickY = frac < 0 ? sheet!.y - 70 : frac === 0 ? row.y + row.height + 10 : mb.y + mb.height * frac;
      const popup = page.locator(".maplibregl-popup");
      await rightClickUntilCallout(page, row.x + 60, clickY);
      await expect(popup).toHaveCount(1);
      // The what-if text lands after the popup opened.
      await expect(popup.locator(".drop-preview")).toContainText("(estimate)");
      const add = page.getByRole("button", { name: "Add as a stop" });
      await expect
        .poll(async () => {
          const b = (await add.boundingBox())!;
          const hit = await page.evaluate(([x, y]) => (document.elementFromPoint(x, y) as HTMLElement | null)?.className ?? "", [b.x + b.width / 2, b.y + b.height / 2]);
          const chips = (await page.locator("[data-map-chips]").boundingBox())!;
          const pb = (await popup.boundingBox())!;
          return hit.includes("drop-add-btn") && (pb.y >= chips.y + chips.height - 2 || pb.x + pb.width <= chips.x || pb.x >= chips.x + chips.width);
        })
        .toBe(true);
      await ctx.close();
    });
  }
}
