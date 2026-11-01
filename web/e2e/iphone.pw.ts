import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { settled } from "./settle";
import { stubApi } from "./stub";
import { e2eNow } from "./clock";

// iPhone-width (390 px) layout checks for the drive and charge charts.
// Screenshots go to e2e/__screenshots__ (git-ignored).
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

for (const [name, href] of [
  ["drive", "/drives/drive-1"],
  ["charge", "/charges/charge-1"],
] as const) {
  test(`${name} detail at 390 px: no horizontal scroll, chart readable`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 });
    const page = await ctx.newPage();
    await stubApi(page);
    await page.goto(href);
    await expect(page.getByRole("heading", { name: new RegExp(name, "i") }).first()).toBeVisible();
    await page.waitForTimeout(1200);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(1);
    await page.screenshot({ path: path.join(SHOTS, `iphone-${name}-detail.png`), fullPage: true });
    await ctx.close();
  });
}

test("charge detail explains a receipt total that differs from this session's cost", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  await stubApi(page);
  // A plug-in receipt: this session plus a folded top-up, so the receipt
  // total ($9.99) is more than this session's own cost ($4.40).
  await page.route("**/api/ledger/monthly**", async (route) => {
    const session = (id: string, cost: number) => ({
      charge_id: id, started_at: new Date(e2eNow()).toISOString(), ended_at: new Date(e2eNow()).toISOString(),
      soc_start: 30, soc_end: 80, kwh: 20, peak_kw: 11, cost, price_per_kwh: 0.11, cost_source: "home_rate",
      final_state: "charging_complete", source: "live", flags: [],
    });
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        version: 1, period: "x", tz: "UTC", currency: "USD", spend: { total: 9.99, by_source: {}, priced_sessions: 2, unpriced_sessions: 0, unpriced_kwh: 0, unpriced_home_rate_estimate: null },
        energy: { sessions: 2, kwh_added: 40, null_energy_sessions: 0, by_kind: {}, },
        driving: { drives: 0, miles: 0, miles_with_energy: 0, kwh_used_known: 0, coverage: null, cost_per_mile: null, cost_per_mile_basis: "spend_over_miles" },
        data_quality: {},
        receipts: [{
          receipt_id: "r1", vehicle_id: "v", kind: "ac", place: "home", km_from_home: 0, started_at: new Date(e2eNow()).toISOString(),
          ended_at: new Date(e2eNow()).toISOString(), local_date: "2026-10-15", spans_days: 1, soc_start: 30, soc_end: 80, kwh: 40, peak_kw: 11,
          cost: 9.99, cost_source: "home_rate", rate: 0.11, session_count: 2, unpriced_sessions: 0, flags: [],
          sessions: [session("charge-1", 4.4), session("charge-top", 5.59)],
        }],
      }),
    });
  });
  await page.goto("/charges/charge-1");
  await expect(page.getByText(/Part of a \$9\.99 receipt with 2 sessions/)).toBeVisible();
  // This session's own figure uses the same $ format as everywhere else.
  await expect(page.getByText("$4.40").first()).toBeVisible();
  await expect(page.getByText("4.40 USD")).toHaveCount(0);
  await ctx.close();
});

test("sample requests always carry an explicit until", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  const urls: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname === "/api/samples") urls.push(r.url());
  });
  await stubApi(page);
  for (const href of ["/charges/charge-1", "/drives/drive-1"]) {
    await page.goto(href);
    await page.waitForTimeout(800);
  }
  expect(urls.length).toBeGreaterThanOrEqual(2);
  for (const u of urls) {
    const q = new URL(u).searchParams;
    expect(q.get("since")).toBeTruthy();
    expect(q.get("until"), u).toBeTruthy();
    expect(Date.parse(q.get("until")!)).toBeGreaterThan(Date.parse(q.get("since")!));
  }
  await ctx.close();
});

test("drive map: the speed legend and attribution never cover the route's end markers @webkit", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  await stubApi(page);
  // A route that ends at the bottom-left corner of its bounds, exactly
  // where the legend sits: top-right start, bottom-left end.
  await page.route("**/api/samples**", async (route) => {
    const url = new URL(route.request().url());
    const since = Date.parse(url.searchParams.get("since") || "") || e2eNow() - 3_600_000;
    const until = Date.parse(url.searchParams.get("until") || "") || since + 3_600_000;
    const out = [];
    for (let t = since, i = 0; t <= until && i < 60; t += 30_000, i++) {
      out.push({
        VehicleID: "veh-sample-1", At: new Date(t).toISOString(), BatteryLevelPct: 70, RangeMi: 200, OdometerMi: 1000 + i,
        Lat: 40 - i * 0.002, Lon: -100 - i * 0.002, SpeedMph: 40 + (i % 5), ShiftState: "D", ChargingState: "",
        ChargerPowerKW: 0, ChargeLimitPct: 80, InsideTempC: 20, OutsideTempC: 12, DriveNumber: 1, ChargeNumber: 0, Source: "live",
      });
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(out) });
  });
  await page.route("**/api/drives**", async (route) => {
    if (new URL(route.request().url()).pathname !== "/api/drives") return route.fallback();
    const t0 = e2eNow() - 3_600_000;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([{
        ID: "drive-1", VehicleID: "veh-sample-1", StartedAt: new Date(t0).toISOString(), EndedAt: new Date(t0 + 1_800_000).toISOString(),
        StartSoCPct: 70, EndSoCPct: 66, StartOdometerMi: 1000, EndOdometerMi: 1012, DistanceMi: 12, StartLat: 40, StartLon: -100,
        EndLat: 39.88, EndLon: -100.12, MaxSpeedMph: 60, AvgSpeedMph: 35, EnergyUsedKWh: 4.8, Source: "live",
      }]),
    });
  });
  await page.goto("/drives/drive-1");
  await expect(page.locator(".rivolt-speed-legend")).toBeVisible();
  // The map fits the route and the tiles settle after the legend is there: measure once nothing is still moving, not after a guessed wait.
  await expect(page.locator(".maplibregl-marker").first()).toBeVisible();
  await settled(page, [".rivolt-speed-legend", ".maplibregl-ctrl-attrib", ".maplibregl-map"]);
  const boxes = await page.evaluate(() => {
    const r = (el: Element) => {
      const b = el.getBoundingClientRect();
      return { l: b.left, t: b.top, r: b.right, b: b.bottom };
    };
    const covers = [...document.querySelectorAll(".rivolt-speed-legend, .maplibregl-ctrl-attrib")].map(r);
    const markers = [...document.querySelectorAll(".maplibregl-marker")].map(r);
    return { covers, markers };
  });
  expect(boxes.markers.length).toBeGreaterThan(0);
  // Overlap, with a 12 px safety margin so a marker isn't left kissing
  // the legend (the zoom snap can leave less room than the fit padding).
  const M = 12;
  const hit = (a: { l: number; t: number; r: number; b: number }, c: { l: number; t: number; r: number; b: number }) =>
    a.l < c.r + M && a.r > c.l - M && a.t < c.b + M && a.b > c.t - M;
  for (const m of boxes.markers) {
    for (const c of boxes.covers) expect(hit(m, c), JSON.stringify({ m, c })).toBe(false);
  }
  await page.locator(".maplibregl-map").first().scrollIntoViewIfNeeded();
  await page.screenshot({ path: path.join(SHOTS, "iphone-drive-map.png"), fullPage: true });
  await ctx.close();
});

test("drive map: basemap toggle doesn't cover the end pin at 390 px @webkit", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  await stubApi(page);
  await page.goto("/drives/drive-1");
  await page.locator(".maplibregl-map").first().scrollIntoViewIfNeeded();
  await expect(page.locator(".maplibregl-marker").first()).toBeVisible();
  await settled(page, [".rivolt-flavor-control", ".maplibregl-map"]);
  const overlap = await page.evaluate(() => {
    const c = document.querySelector(".rivolt-flavor-control")!.getBoundingClientRect();
    const pins = [...document.querySelectorAll(".maplibregl-marker")].map((e) => e.getBoundingClientRect());
    return pins.some((p) => p.left < c.right && p.right > c.left && p.top < c.bottom && p.bottom > c.top);
  });
  expect(overlap).toBe(false);
  await ctx.close();
});
