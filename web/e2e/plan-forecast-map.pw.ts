import fs from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { DEMO_PLAN, stubApi } from "./stub";

// The planner map's forecast radar (design key plan_forecast_map). Pro with the key on: the Radar button draws NOAA RRFS for an hour of the trip, with
// a scrubber (one step an hour, leave to arrive), the truck at that hour, and one plain line where the forecast does not reach. Free (402), key off,
// forecast feed down: the live radar as before / one line. FORECAST_SHOTS=<dir> also writes the stills.
const pt = (label: string, lat: number, lon: number) => ({ lat, lon, label });
const INPUTS = { origin: pt("Los Angeles", 34.05, -118.24), destination: pt("Las Vegas", 36.17, -115.14), extra_stops: [], overnight_flags: [] };
const j = (b: unknown, status = 200) => ({ status, contentType: "application/json", body: JSON.stringify(b) });
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64");

// Leaves 16:20Z, charges 16:40-17:05, arrives 20:28Z: the scrubber has steps 16:00 to 20:00 (five).
const T = (iso: string) => iso;
const TIMED = {
  ...DEMO_PLAN,
  Routes: DEMO_PLAN.Routes.map((r) => ({
    ...r,
    Waypoints: r.Waypoints.map((w, i) => ({
      ...w,
      ...(i === 0 ? { DepartureTimeUTC: T("2026-12-01T16:20:00Z") } : i === 1 ? { ArrivalTimeUTC: T("2026-12-01T16:40:00Z"), DepartureTimeUTC: T("2026-12-01T17:05:00Z") } : { ArrivalTimeUTC: T("2026-12-01T20:28:00Z") }),
    })),
  })),
};
const H0 = Date.parse("2026-12-01T16:00:00Z") / 1000;
// The forecast has hours 16:00, 17:00, 18:00 (fh 0 to 2): the 19:00 and 20:00 steps are past its horizon.
const HOURS = { cycle: "2026120112", credit: "NOAA RRFS", bounds: [-125, 21, -66, 53], hours: [0, 1, 2].map((fh) => ({ fh, valid_unix: H0 + fh * 3600 })), horizon_unix: H0 + 2 * 3600 };
const ME = { user_id: "u-sample", username: "sample@example.com", role: "user", onboarding_completed: true, billing_enabled: true, history_days: null, plan_trial_ends: null, plan_cancels_at: null, plan_grace_ends: null, plan_ended_at: null, plan_ended_source: null };

type Opts = { keys?: string[]; hours?: { status: number; body?: unknown }; view?: { width: number; height: number }; scheme?: "light" | "dark"; untimed?: boolean; flaky?: boolean };
async function open(page: Page, o: Opts = {}) {
  const keys = o.keys ?? ["plan_forecast_map"];
  const tiles: string[] = [];
  let frames = 0;
  let hoursCalls = 0;
  await stubApi(page, { designPreview: keys });
  await page.route("**/api/config", (r) =>
    r.fulfill(j({
      features: { trip_planner_enabled: true, landing_enabled: false, demo_enabled: false, signup_enabled: true, design_preview: keys },
      tiles: { url: "/api/maps/tiles/us.pmtiles", chargers_url: "/api/maps/tiles/chargers.pmtiles", glyphs_url: "/api/maps/glyphs/{fontstack}/{range}.pbf", dem_url: "/api/maps/dem/{z}/{x}/{y}.png", dem_minzoom: 3, dem_maxzoom: 10, radar_url: "/api/maps/radar/{t}/{z}/{x}/{y}.png", radar_frame_url: "/api/maps/radar/frame", radar_minzoom: 3, radar_maxzoom: 9 },
      valhalla: { path: "/api/maps/valhalla" },
      impersonation: { disabled: true },
    })),
  );
  await page.route("**/api/auth/me", (r) => r.fulfill(j({ ...ME, plan: "pro", plan_source: "stripe", plan_period_end: "2026-12-20T00:00:00Z" })));
  await page.route("**/api/maps/radar/frame", (r) => { frames++; return r.fulfill(j({ frame_unix: Math.floor(Date.now() / 1000) - 300 })); });
  await page.route("**/api/maps/radar/*/*/*/*.png", (r) => r.fulfill({ status: 200, contentType: "image/png", body: PNG }));
  await page.route("**/api/maps/forecast-radar/hours", (r) => { hoursCalls++; const h = o.flaky && hoursCalls === 1 ? { status: 503, body: { reason: "feed_down" } } : (o.hours ?? { status: 200, body: HOURS }); return r.fulfill(j(h.body ?? { reason: "x" }, h.status)); });
  await page.route("**/api/maps/forecast-radar/*/*/*/*/*.png", (r) => { tiles.push(new URL(r.request().url()).pathname); return r.fulfill({ status: 200, contentType: "image/png", body: PNG }); });
  await page.route("**/api/trips/saved", (r) => r.fulfill(j([{ id: "st-a", name: "Old trip", inputs: INPUTS, created_at: "2026-10-17T00:00:00Z", updated_at: "2026-10-17T00:00:00Z" }])));
  await page.route("**/api/trips/plan", (r) => r.fulfill(j(o.untimed ? DEMO_PLAN : TIMED)));
  await page.goto("/trips/plan");
  await page.getByRole("tab", { name: "Saved" }).click();
  await page.getByRole("button", { name: /Old trip/ }).click();
  await page.getByRole("tab", { name: "Planner" }).click();
  await page.getByRole("button", { name: "Plan trip" }).click();
  await expect(page.getByText(/Sample Fast Charge, Barstow/).first()).toBeVisible();
  return { tiles, frames: () => frames, hoursCalls: () => hoursCalls };
}
const radarButton = (page: Page) => page.locator("[data-radar-chip]");
const slider = (page: Page) => page.locator("[data-forecast-slider]");
const setStep = async (page: Page, v: number) => {
  await slider(page).evaluate((el, n) => {
    const i = el as HTMLInputElement;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(i, String(n));
    i.dispatchEvent(new Event("input", { bubbles: true }));
  }, v);
};

test("@webkit Pro with the key on: the forecast for the departure hour, a scrubber, the credit, no live radar", async ({ page }) => {
  const seen = await open(page);
  await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true"); // the Trip planner settings start it on
  await expect(slider(page)).toBeVisible();
  await expect(slider(page)).toHaveAttribute("max", "4");
  await expect(page.locator("[data-radar-label]")).toContainText("NOAA RRFS · Forecast for");
  await expect.poll(() => seen.tiles.find((t) => t.startsWith("/api/maps/forecast-radar/2026120112/0/"))).toBeTruthy();
  expect(seen.frames()).toBe(0);
  await expect(page.locator("[data-forecast-line]")).toHaveCount(0);
  await expect(page.locator("[data-forecast-truck]")).toHaveCount(1);
});

test("@webkit moving the scrubber asks for that hour once it settles, and a quick sweep does not ask for every hour", async ({ page }) => {
  const seen = await open(page);
  await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true"); // the Trip planner settings start it on
  await expect.poll(() => seen.tiles.some((t) => t.includes("/2026120112/0/"))).toBe(true);
  await setStep(page, 1);
  await page.waitForTimeout(250); // a sweep: the next step comes before the scrubber has been still for 300 ms
  await setStep(page, 2);
  await expect.poll(() => seen.tiles.some((t) => t.includes("/2026120112/2/"))).toBe(true);
  expect(seen.tiles.some((t) => t.includes("/2026120112/1/"))).toBe(false);
});

test("@webkit past the forecast's horizon: no layer, one plain line, the slider still moves", async ({ page }) => {
  const seen = await open(page);
  await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true"); // the Trip planner settings start it on
  await expect(slider(page)).toBeVisible();
  await setStep(page, 4);
  await expect(page.locator("[data-forecast-line]")).toHaveText("The forecast doesn't reach this hour.");
  // No picture for this hour: just the hour, no legend, credit or "Forecast for" under the line.
  await expect(page.locator("[data-radar-label]")).toHaveText(/^\d{1,2}\s?[ap]m$/i);
  await expect(page.locator("[data-radar-label] [data-strip-bar]")).toHaveCount(0);
  await page.waitForTimeout(800); // the scrubber has settled and the old hour's last tiles are in
  const before = seen.tiles.length;
  await page.waitForTimeout(800);
  expect(seen.tiles.length).toBe(before);
  expect(seen.tiles.every((t) => t.includes("/2026120112/0/"))).toBe(true); // nothing was ever asked for hour 3 or 4
  await setStep(page, 1);
  await expect(page.locator("[data-forecast-line]")).toHaveCount(0);
});

test("a theme flip replaces the map style: the forecast layer comes back", async ({ page }) => {
  const seen = await open(page);
  await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => seen.tiles.length).toBeGreaterThan(0);
  await page.waitForTimeout(800);
  const before = seen.tiles.length;
  await page.evaluate(() => {
    const r = document.documentElement;
    r.setAttribute("data-theme", r.getAttribute("data-theme") === "light" ? "dark" : "light");
  });
  await expect.poll(() => seen.tiles.length, { timeout: 8000 }).toBeGreaterThan(before); // the hour is asked for again on the new style
  await expect(page.locator("[data-radar-label]")).toContainText("NOAA RRFS · Forecast for");
});

test("a plan without times: no scrubber, no false line, the live radar as before", async ({ page }) => {
  const seen = await open(page, { untimed: true });
  await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true");
  await expect(slider(page)).toHaveCount(0);
  await expect(page.locator("[data-forecast-line]")).toHaveCount(0);
  await expect(page.locator("[data-radar-label]")).toContainText("NOAA/NWS");
  expect(seen.tiles).toEqual([]);
});

test("a blip on the hours is asked again within a minute", async ({ page }) => {
  test.setTimeout(90000);
  const seen = await open(page, { flaky: true });
  await expect(page.locator("[data-forecast-line]")).toHaveText("Forecast unavailable");
  await expect(slider(page)).toBeVisible({ timeout: 60000 });
  expect(seen.hoursCalls()).toBe(2);
});

test("@webkit a trip outside the forecast's grid: no layer, one plain line", async ({ page }) => {
  const seen = await open(page, { hours: { status: 200, body: { ...HOURS, bounds: [-10, 35, 30, 60] } } });
  await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true"); // the Trip planner settings start it on
  await expect(page.locator("[data-forecast-line]")).toHaveText("The forecast covers the contiguous US.");
  await expect(slider(page)).toBeVisible();
  await page.waitForTimeout(800);
  expect(seen.tiles).toEqual([]);
});

test("the forecast feed down: one plain line, never the live picture in its place", async ({ page }) => {
  const seen = await open(page, { hours: { status: 503, body: { reason: "feed_down" } } });
  await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true"); // the Trip planner settings start it on
  await expect(page.locator("[data-forecast-line]")).toHaveText("Forecast unavailable");
  await expect(slider(page)).toHaveCount(0);
  expect(seen.frames()).toBe(0);
});

test("free (402 pro_required): the live radar as before, nothing about a forecast", async ({ page }) => {
  const seen = await open(page, { hours: { status: 402, body: { error: "pro_required" } } });
  await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true"); // the Trip planner settings start it on
  await expect(page.locator("[data-radar-label]")).toContainText("NOAA/NWS");
  expect(seen.frames()).toBeGreaterThan(0);
  await expect(slider(page)).toHaveCount(0);
  await expect(page.locator("[data-forecast-line]")).toHaveCount(0);
  expect(seen.tiles).toEqual([]);
});

test("key off: the live radar as before, the forecast route is never asked", async ({ page }) => {
  const seen = await open(page, { keys: [] });
  await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true"); // the Trip planner settings start it on
  await expect(page.locator("[data-radar-label]")).toContainText("NOAA/NWS");
  expect(seen.hoursCalls()).toBe(0);
  await expect(slider(page)).toHaveCount(0);
});

const SHOTS = process.env.FORECAST_SHOTS;
// The map is below the fold on a desktop page: a still of the map itself on desktop, the whole phone screen on a phone.
const shot = async (page: Page, path: string) => {
  if ((page.viewportSize()?.width ?? 0) >= 1024) await page.locator("[data-plan-area='map']").screenshot({ path });
  else await page.screenshot({ path });
};
for (const [name, view] of [["phone-393", { width: 393, height: 852 }], ["desktop-1440", { width: 1440, height: 900 }]] as const) {
  for (const scheme of ["light", "dark"] as const) {
    test(`stills ${name} ${scheme}`, async ({ browser }) => {
      test.skip(!SHOTS, "FORECAST_SHOTS not set");
      const ctx = await browser.newContext({ viewport: view, colorScheme: scheme });
      const page = await ctx.newPage();
      await open(page);
      await expect(radarButton(page)).toHaveAttribute("aria-pressed", "true"); // the Trip planner settings start it on
      await expect(slider(page)).toBeVisible();
      await page.waitForTimeout(1200);
      fs.mkdirSync(SHOTS!, { recursive: true });
      await shot(page, `${SHOTS}/forecast-${name}-${scheme}.png`);
      await setStep(page, 4);
      await expect(page.locator("[data-forecast-line]")).toBeVisible();
      await shot(page, `${SHOTS}/forecast-${name}-${scheme}-past-horizon.png`);
      await ctx.close();
    });
  }
}
