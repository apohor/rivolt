import fs from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi, VEHICLE_ID } from "./stub";

// design_preview "plan_where_to": the planner opens on "Where to?" (no map), the truck's range and the saved trips; typing lists places and the plans
// that match; the map comes with a route. Without the key the page is what it was. SHOTS=<dir> also writes the screenshots the PR carries (widths 320/393/430/desktop, light and dark, the states).
const SHOTS = process.env.SHOTS || "";
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Rose Bowl parking", 34.16, -118.17), destination: pt("Las Vegas, NV", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const SAVED = ["Vegas weekend", "Pasadena to Kettleman City", "Mammoth with a very long name that has to wrap, not clip"].map((name, i) => ({ id: `st-${i}`, name, inputs: INPUTS, plan: DEMO_PLAN, created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-0" + (i + 1) + "T00:00:00Z" }));
const json = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });

const TWO_DAY = {
  id: "la-vegas-2day",
  name: "Las Vegas, 2 days",
  inputs: { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Los Angeles", 34.05, -118.24), extra_stops: [pt("Sample Inn, Las Vegas", 36.17, -115.14)], overnight_flags: [true], starting_soc: "90", target_soc: "20" },
  summary: { distance_mi: 530, duration_min: 490, charge_stops: 2, arrival_soc_pct: 24 },
  saved_trip_id: "st-demo-2day",
};

async function open(page: Page, o: { flag?: boolean; saved?: boolean; demo?: boolean; paused?: boolean; truck?: boolean; geocode?: unknown; geocodeStatus?: number }) {
  await stubApi(page, { designPreview: o.flag === false ? [] : ["plan_where_to"], demo: !!o.demo, signedIn: !o.demo, tripShare: true, ...(o.paused ? { rivian: "paused" as const } : {}) });
  if (!o.demo) await page.route("**/api/trips/saved", (r) => r.fulfill(json(o.saved === false ? [] : SAVED)));
  if (o.demo) await page.route("**/api/demo/trips", (r) => r.fulfill(json({ trips: [...DEMO_PRESETS, TWO_DAY] })));
  await page.route("**/api/geocode*", (r) =>
    o.geocodeStatus ? r.fulfill({ status: o.geocodeStatus, contentType: "application/json", body: "{}" }) : r.fulfill(json(o.geocode ?? [{ name: "Las Vegas", admin1: "Nevada", country: "US", latitude: 36.17, longitude: -115.14 }])),
  );
  if (o.truck) await page.route("**/api/vehicles/owned", (r) => r.fulfill(json({ vehicles: [{ id: "o1", rivian_vehicle_id: VEHICLE_ID, display_name: "Sample R1S", model: "R1S" }] })));
  await page.route("**/api/trips/saved/*", (r) => (r.request().method() === "PUT" ? r.fulfill(json({ ...SAVED[0], plan: DEMO_PLAN })) : r.fallback()));
  await page.route("**/api/trips/saved/*/shares", (r) => r.fulfill(json({ links: [] })));
  await page.route("**/api/trips/saved", (r) => (r.request().method() === "POST" ? r.fulfill(json({ id: "st-new", name: "Rose Bowl parking to Las Vegas, NV", inputs: INPUTS, plan: DEMO_PLAN, created_at: "2026-11-20T00:00:00Z", updated_at: "2026-11-20T00:00:00Z" })) : r.fallback()));
  await page.route("**/api/trips/plan", (r) => r.fulfill(json(DEMO_PLAN)));
  await page.goto(o.demo ? "/demo/trips/plan" : "/trips/plan");
}
const shot = async (page: Page, name: string) => {
  if (!SHOTS) return;
  fs.mkdirSync(SHOTS, { recursive: true });
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: true });
};
const noOverflow = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
// Everything of the page (the app header's own 7 px at 300 % text is not this page's) inside the screen's width.
const wideOnes = (page: Page) =>
  page.evaluate(() => [...document.querySelectorAll("body *")].filter((e) => !e.closest("header") && e.getBoundingClientRect().right > window.innerWidth + 1).slice(0, 5).map((e) => `${e.tagName}.${String(e.className).slice(0, 60)}`));

test("without the key the planner is unchanged", async ({ page }) => {
  await open(page, { flag: false });
  await expect(page.locator("[data-where-to]")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Trip planner" })).toBeVisible();
  await expect(page.locator("[data-plan-form]")).toBeVisible();
});

test("opening state @webkit: the field first, the range, saved trips, no map and no sample trips", async ({ page }) => {
  await open(page, { truck: true });
  const home = page.locator("[data-where-to]");
  await expect(home).toBeVisible();
  await expect(page.getByRole("heading", { name: "Plan", exact: true })).toBeVisible();
  await expect(page.locator("[data-plan-form]")).toHaveCount(0);
  await expect(page.locator(".maplibregl-map")).toHaveCount(0);
  await expect(page.getByText("Sample trips")).toHaveCount(0);
  // The field comes before the chips and the range.
  const field = (await page.getByRole("textbox", { name: "Where to?" }).boundingBox())!;
  const chip = (await page.getByRole("button", { name: "Leave now" }).boundingBox())!;
  expect(field.y).toBeLessThan(chip.y);
  // iOS Safari zooms the page into a field whose text is under 16 px (the page then sits shifted left with the keyboard up).
  expect(await page.getByRole("textbox", { name: "Where to?" }).evaluate((e) => parseFloat(getComputedStyle(e).fontSize))).toBeGreaterThanOrEqual(16);
  await expect(page.locator("[data-range-line]")).toHaveText(/^\d+ mi of range, from where your truck is parked now$/);
  await expect(page.getByRole("heading", { name: "Recent and saved" })).toBeVisible();
  await expect(page.locator("[data-trip-row]")).toHaveCount(3);
  await expect(page.getByRole("button", { name: /Saved trips \(3\)/ })).toBeVisible();
});

test("the range line is hidden when the truck's state is old", async ({ page }) => {
  await open(page, { truck: true });
  await page.route(`**/api/state/${VEHICLE_ID}`, (r) =>
    r.fulfill(json({ at: "2026-11-01T00:00:00Z", battery_level_pct: 80, distance_to_empty: 300, power_state: "ready", gear: "P", latitude: 40, longitude: -100 })),
  );
  await page.reload();
  await expect(page.locator("[data-where-to]")).toBeVisible();
  await expect(page.locator("[data-range-line]")).toHaveCount(0);
});

test("typing @webkit: places and matching plans under the field, a pick leaves the front door", async ({ page }) => {
  await open(page, {});
  await page.getByRole("textbox", { name: "Where to?" }).fill("Las Vegas");
  await expect(page.locator("[data-where-results]")).toBeVisible();
  // The opening content is gone while typing.
  await expect(page.getByRole("heading", { name: "Recent and saved" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Las Vegas Nevada, US/ })).toBeVisible();
  await page.getByRole("button", { name: /^Las Vegas Nevada, US/ }).click();
  // The stub truck has no position, so there is no start to plan from: the form opens with the destination filled in.
  await expect(page.locator("[data-where-to]")).toHaveCount(0);
  await expect(page.locator("[data-plan-form]")).toContainText("Las Vegas");
});

test("typing: a plan already made is listed, and opens", async ({ page }) => {
  await open(page, {});
  await page.getByRole("textbox", { name: "Where to?" }).fill("vegas");
  const plans = page.getByRole("region", { name: "Your plans" });
  await expect(plans.getByRole("button", { name: /Vegas weekend/ }).first()).toBeVisible();
  await plans.getByRole("button", { name: /Vegas weekend/ }).first().click();
  await expect(page.locator("[data-where-to]")).toHaveCount(0);
  await expect(page.locator("[data-plan-area='map']")).toBeVisible();
});

test("typing: searching, no results and an error each say so", async ({ page }) => {
  await open(page, { geocode: [] });
  await page.getByRole("textbox", { name: "Where to?" }).fill("big bend");
  await expect(page.getByText('No places match "big bend"')).toBeVisible();
  await page.getByRole("button", { name: "Clear" }).click();
  await expect(page.getByRole("heading", { name: "Recent and saved" })).toBeVisible();
  const slow = await page.context().newPage();
  await open(slow, { geocodeStatus: 500 });
  await slow.getByRole("textbox", { name: "Where to?" }).fill("big bend");
  await expect(slow.getByRole("alert")).toContainText("Place search is unavailable", { timeout: 20_000 });
  await slow.close();
});

test("typing: a search in flight says Searching", async ({ page }) => {
  await open(page, {});
  await page.route("**/api/geocode*", async (r) => {
    await new Promise((res) => setTimeout(res, 800));
    await r.fulfill(json([]));
  });
  await page.getByRole("textbox", { name: "Where to?" }).fill("big bend");
  await expect(page.getByRole("status")).toContainText("Searching");
});

test("a saved trip opens its plan, Back returns to the front door", async ({ page }) => {
  await open(page, {});
  await page.getByRole("button", { name: /Vegas weekend/ }).first().click();
  await expect(page.locator("[data-where-to]")).toHaveCount(0);
  await expect(page.locator("[data-plan-area='map']")).toBeVisible();
  await page.getByRole("button", { name: /Where to\?/ }).click();
  await expect(page.locator("[data-where-to]")).toBeVisible();
  await expect(page.locator("[data-plan-area='map']")).toHaveCount(0);
});

test("no saved trips: a hint, no empty list", async ({ page }) => {
  await open(page, { saved: false });
  await expect(page.getByText("Trips you plan and save show up here")).toBeVisible();
  await expect(page.locator("[data-trip-row]")).toHaveCount(0);
});

test("share: a saved row has a 44 px Share button that opens the sheet, away from delete", async ({ page }) => {
  await open(page, {});
  const share = page.getByRole("button", { name: /^Share "Vegas weekend"/ });
  const b = (await share.boundingBox())!;
  expect(b.width).toBeGreaterThanOrEqual(44);
  expect(b.height).toBeGreaterThanOrEqual(44);
  await share.click();
  await expect(page.getByTestId("trip-share-sheet")).toBeVisible();
});

test("share on the route screen saves the re-planned trip first and says so", async ({ page }) => {
  await open(page, {});
  await page.getByRole("button", { name: /Vegas weekend/ }).first().click();
  // A re-plan makes it a trip that is not saved as shown.
  await page.getByRole("button", { name: "Re-plan now" }).click();
  await expect(page.getByTestId("route-share")).toBeVisible();
  const b = (await page.getByTestId("route-share").boundingBox())!;
  expect(b.height).toBeGreaterThanOrEqual(44);
  await page.getByTestId("route-share").click();
  await expect(page.getByRole("status").filter({ hasText: "so it can be shared" })).toBeVisible();
  await expect(page.getByTestId("trip-share-sheet")).toBeVisible();
});

test("signed out: the demo keeps its sample trips page", async ({ page }) => {
  await open(page, { demo: true });
  await expect(page.locator("[data-where-to]")).toHaveCount(0);
  await expect(page.getByTestId("demo-presets").getByRole("button").first()).toBeVisible();
});

// Nothing sideways: the page is no wider than the screen at the phone widths (the amber paused-connection banner included), with long unbroken
// names, typed results, and on a desktop; light and dark.
for (const w of [320, 393, 430, 1280, 1440]) {
  for (const scheme of ["dark", "light"] as const) {
    test(`layout ${w} px ${scheme}: no sideways scroll, nothing clipped @webkit`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w > 1000 ? 860 : 800 }, colorScheme: scheme });
      const page = await ctx.newPage();
      await open(page, { paused: true, geocode: [{ name: "Pneumonoultramicroscopicsilicovolcanoconiosis_Station_Number_Seventy_Seven", admin1: "Nevada", country: "US", latitude: 36.1, longitude: -115.1 }] });
      await expect(page.locator("[data-where-to]")).toBeVisible();
      expect(await noOverflow(page)).toBe(true);
      expect(await wideOnes(page)).toEqual([]);
      const b = (await page.locator("[data-where-to]").boundingBox())!;
      expect(b.x).toBeGreaterThanOrEqual(0);
      expect(b.x + b.width).toBeLessThanOrEqual(w + 0.5);
      // One column of at most 720 px, no framed empty area beside it.
      expect(b.width).toBeLessThanOrEqual(720.5);
      await shot(page, `open-${w}-${scheme}`);
      await page.getByRole("textbox", { name: "Where to?" }).fill("pneumo");
      await expect(page.getByRole("button", { name: /Pneumono/ })).toBeVisible();
      expect(await noOverflow(page)).toBe(true);
      expect(await wideOnes(page)).toEqual([]);
      await shot(page, `typing-${w}-${scheme}`);
      await page.getByRole("button", { name: "Clear" }).click();
      await page.getByRole("button", { name: /Vegas weekend/ }).first().click();
      await expect(page.locator("[data-plan-area='map']")).toBeVisible();
      expect(await noOverflow(page)).toBe(true);
      await shot(page, `route-${w}-${scheme}`);
      await ctx.close();
    });
  }
}

test("no saved trips and the departure picker (393, dark and light)", async ({ browser }) => {
  for (const scheme of ["dark", "light"] as const) {
    const ctx = await browser.newContext({ viewport: { width: 393, height: 800 }, colorScheme: scheme });
    const page = await ctx.newPage();
    await open(page, { saved: false });
    await shot(page, `none-393-${scheme}`);
    await page.getByRole("button", { name: "Leave now" }).click();
    await shot(page, `departure-393-${scheme}`);
    await ctx.close();
  }
});

for (const zoom of [200, 300]) {
  test(`large text ${zoom}%: the page grows, nothing of the page is cut at the sides`, async ({ browser }) => {
    // Safari's text size scales the root font.
    const ctx = await browser.newContext({ viewport: { width: 393, height: 800 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, { paused: true });
    await page.addStyleTag({ content: `html{font-size:${zoom * 0.16}px !important}` });
    await expect(page.locator("[data-where-to]")).toBeVisible();
    await shot(page, `text${zoom}-open-393-dark`);
    expect(await wideOnes(page)).toEqual([]);
    await page.getByRole("textbox", { name: "Where to?" }).fill("vegas");
    await expect(page.getByRole("button", { name: /^Las Vegas Nevada/ })).toBeVisible();
    await shot(page, `text${zoom}-typing-393-dark`);
    expect(await wideOnes(page)).toEqual([]);
    await ctx.close();
  });
}
