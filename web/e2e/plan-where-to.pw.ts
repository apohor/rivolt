import fs from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, DEMO_PRESETS, stubApi } from "./stub";

// design_preview "plan_where_to": the planner opens on the map with "Where to?", the saved trips and the samples under it. Without the key the page
// is what it was. SHOTS=<dir> also writes the screenshots the PR carries (widths 320/393/430/desktop, light and dark, the states).
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

async function open(page: Page, o: { flag?: boolean; saved?: boolean; demo?: boolean }) {
  await stubApi(page, { designPreview: o.flag === false ? [] : ["plan_where_to"], demo: !!o.demo, signedIn: !o.demo });
  if (!o.demo) await page.route("**/api/trips/saved", (r) => r.fulfill(json(o.saved === false ? [] : SAVED)));
  if (o.demo) await page.route("**/api/demo/trips", (r) => r.fulfill(json({ trips: [...DEMO_PRESETS, TWO_DAY] })));
  await page.route("**/api/geocode*", (r) => r.fulfill(json([{ name: "Las Vegas", admin1: "Nevada", country: "US", latitude: 36.17, longitude: -115.14 }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(json(DEMO_PLAN)));
  await page.goto(o.demo ? "/demo/trips/plan" : "/trips/plan");
}
const shot = async (page: Page, name: string) => {
  if (!SHOTS) return;
  fs.mkdirSync(SHOTS, { recursive: true });
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: true });
};
const noOverflow = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);

test("without the key the planner is unchanged", async ({ page }) => {
  await open(page, { flag: false });
  await expect(page.locator("[data-where-to]")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Trip planner" })).toBeVisible();
  await expect(page.locator("[data-plan-form]")).toBeVisible();
});

test("with the key: Where to? over the map @webkit, saved trips below, a pick plans the trip", async ({ page }) => {
  await open(page, {});
  await expect(page.locator("[data-where-to]")).toBeVisible();
  await expect(page.locator("[data-plan-form]")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Recent and saved" })).toBeVisible();
  // Each saved row leads with its icon tile, as in the mock.
  await expect(page.locator("[data-trip-icon]")).toHaveCount(3);
  await expect(page.getByRole("button", { name: /Saved trips \(3\)/ })).toBeVisible();
  await page.getByRole("textbox", { name: "Where to?" }).fill("Las Vegas");
  await page.getByRole("button", { name: /^Las Vegas Nevada/ }).click();
  // The stub truck has no position, so there is no start to plan from: the form opens with the destination filled in.
  await expect(page.locator("[data-where-to]")).toHaveCount(0);
  await expect(page.locator("[data-plan-form]")).toContainText("Las Vegas");
});

test("with the key: a saved trip opens its plan", async ({ page }) => {
  await open(page, {});
  await page.getByRole("button", { name: /Vegas weekend/ }).click();
  await expect(page.locator("[data-where-to]")).toHaveCount(0);
  await expect(page.locator("[data-plan-area='map']")).toBeVisible();
});

test("with the key, no saved trips: a hint, no empty list", async ({ page }) => {
  await open(page, { saved: false });
  await expect(page.getByText("Trips you save show up here")).toBeVisible();
});

test("with the key, signed out: the entry filters the samples and a sample opens", async ({ page }) => {
  await open(page, { demo: true });
  await expect(page.locator("[data-where-to]")).toBeVisible();
  const samples = page.getByTestId("demo-presets").getByRole("button");
  await expect(samples.first()).toBeVisible();
  // The multi-day sample comes first and says what it is, as in the mock: "530 mi · hotel night".
  await expect(samples.first()).toContainText("Las Vegas, 2 days");
  await expect(samples.first()).toContainText("530 mi · hotel night");
  await page.getByRole("searchbox", { name: /Where to/ }).fill("zzzz");
  await expect(page.getByText("No sample trip matches that.")).toBeVisible();
  await page.getByRole("searchbox", { name: /Where to/ }).fill("");
  await samples.filter({ hasText: "LA to Las Vegas" }).click();
  await expect(page.locator("[data-where-to]")).toHaveCount(0);
});

for (const w of [320, 393, 430, 1280]) {
  for (const scheme of ["dark", "light"] as const) {
    test(`layout ${w} px ${scheme}: nothing clipped, no sideways scroll @webkit`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: w, height: w > 1000 ? 860 : 800 }, colorScheme: scheme });
      const page = await ctx.newPage();
      await open(page, {});
      await expect(page.locator("[data-where-to]")).toBeVisible();
      expect(await noOverflow(page)).toBe(true);
      // The entry, the chips and every row sit inside the screen's width.
      for (const sel of ["[data-where-card]", "[data-where-lists]"]) {
        const b = (await page.locator(sel).first().boundingBox())!;
        expect(b.x).toBeGreaterThanOrEqual(0);
        expect(b.x + b.width).toBeLessThanOrEqual(w + 0.5);
      }
      await shot(page, `saved-${w}-${scheme}`);
      await ctx.close();
    });
  }
}

test("states: no saved trips, signed-out demo, a planned route (393, dark and light)", async ({ browser }) => {
  for (const scheme of ["dark", "light"] as const) {
    const ctx = await browser.newContext({ viewport: { width: 393, height: 800 }, colorScheme: scheme });
    const page = await ctx.newPage();
    await open(page, { saved: false });
    await shot(page, `none-393-${scheme}`);
    await page.getByRole("button", { name: "Leave now" }).click();
    await shot(page, `departure-393-${scheme}`);
    await ctx.close();
    const c2 = await browser.newContext({ viewport: { width: 393, height: 800 }, colorScheme: scheme });
    const p2 = await c2.newPage();
    await open(p2, { demo: true });
    await shot(p2, `demo-393-${scheme}`);
    await p2.getByTestId("demo-presets").getByRole("button", { name: /LA to Las Vegas/ }).click();
    await expect(p2.locator("[data-where-to]")).toHaveCount(0);
    await shot(p2, `demo-route-393-${scheme}`);
    await c2.close();
  }
});

for (const zoom of [200, 300]) {
  test(`large text ${zoom}%: the page grows, nothing of the page is cut at the sides (the app header's own 7 px at 300 % is not this page's)`, async ({ browser }) => {
    // Safari's text size scales the root font; the same box the 200 % and 300 % specs use.
    const ctx = await browser.newContext({ viewport: { width: zoom === 200 ? 393 : 393, height: 800 }, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, {});
    await page.addStyleTag({ content: `html{font-size:${zoom * 0.16}px !important}` });
    await expect(page.locator("[data-where-to]")).toBeVisible();
    await shot(page, `text${zoom}-393-dark`);
    const wide = await page.evaluate(() => [...document.querySelectorAll("body *")].filter((e) => !e.closest("header") && e.getBoundingClientRect().right > window.innerWidth + 1).slice(0, 5).map((e) => `${e.tagName}.${String(e.className).slice(0, 60)}`));
    expect(wide, wide.join("\n")).toEqual([]);
    await ctx.close();
  });
}
