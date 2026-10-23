import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { stubApi, VEHICLE_ID } from "./stub";

// A drive with no second-by-second samples (a gap or an import) still has its stored route: the map is drawn from it, and
// the empty states say what is missing and what is there, with no claim about age.

function encode5(pts: [number, number][]): string {
  let pl = 0;
  let po = 0;
  let out = "";
  const enc = (v: number) => {
    let n = v < 0 ? ~(v << 1) : v << 1;
    let s = "";
    while (n >= 0x20) {
      s += String.fromCharCode((0x20 | (n & 0x1f)) + 63);
      n >>= 5;
    }
    return s + String.fromCharCode(n + 63);
  };
  for (const [lat, lon] of pts) {
    const la = Math.round(lat * 1e5);
    const lo = Math.round(lon * 1e5);
    out += enc(la - pl) + enc(lo - po);
    pl = la;
    po = lo;
  }
  return out;
}
const ROUTE = encode5([[40.0, -100.0], [40.1, -100.2], [40.25, -100.3], [40.4, -100.6]]);
const ago = (days: number) => new Date(Date.now() - days * 86_400_000).toISOString();
const drive = (id: string, days: number, route: string) => ({
  ID: id, VehicleID: VEHICLE_ID, StartedAt: ago(days), EndedAt: ago(days - 0.02), StartSoCPct: 70, EndSoCPct: 60, StartOdometerMi: 100, EndOdometerMi: 130,
  DistanceMi: 30, StartLat: 40, StartLon: -100, EndLat: 40.4, EndLon: -100.6, MaxSpeedMph: 70, AvgSpeedMph: 40, EnergyUsedKWh: 10, Source: "live", RoutePolyline: route,
});
const charge = (id: string, days: number) => ({
  ID: id, VehicleID: VEHICLE_ID, StartedAt: ago(days), EndedAt: ago(days - 0.1), StartSoCPct: 30, EndSoCPct: 80, EnergyAddedKWh: 40, MilesAdded: 120,
  MaxPowerKW: 11, AvgPowerKW: 10, FinalState: "complete", Lat: 40, Lon: -100, Source: "live", Cost: 6, Currency: "USD", PricePerKWh: 0.15,
});

async function open(page: Page, path: string) {
  await stubApi(page);
  const j = (b: unknown) => ({ status: 200, contentType: "application/json", body: JSON.stringify(b) });
  const all = [drive("old-route", 200, ROUTE), drive("old-bare", 200, ""), drive("new-bare", 1, "")];
  // The list leaves routes out (slim=1); one drive comes with its route.
  await page.route(/\/api\/drives(\?|$)/, (r) => r.fulfill(j(new URL(r.request().url()).searchParams.get("slim") === "1" ? all.map((d) => ({ ...d, RoutePolyline: "" })) : all)));
  await page.route(/\/api\/drives\/[^/?]+$/, (r) => {
    const id = new URL(r.request().url()).pathname.split("/").pop();
    const d = all.find((x) => x.ID === id);
    return d ? r.fulfill(j(d)) : r.fulfill({ status: 404, contentType: "application/json", body: "{}" });
  });
  await page.route(/\/api\/charges(\?|$)/, (r) => r.fulfill(j([charge("old-charge", 200), charge("new-charge", 1)])));
  await page.route(/\/api\/samples/, (r) => r.fulfill(j([])));
  await page.goto(path);
}

for (const v of [
  { n: "iphone", vp: { width: 390, height: 844 } },
  { n: "desktop", vp: { width: 1280, height: 900 } },
] as const) {
  test(`a drive with no samples draws its stored route and says what is there · ${v.n}`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: v.vp, colorScheme: "dark" });
    const page = await ctx.newPage();
    await open(page, "/drives/old-route");
    await expect(page.locator("[data-no-samples='route']").first()).toContainText("Detailed data isn't available for this drive; its route and figures are.");
    // The map is drawn from the stored route: no "no telemetry" message in the Route card.
    await expect(page.locator(".leaflet-container").first()).toBeVisible();
    await expect(page.getByText(/No telemetry was recorded/)).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.waitForTimeout(1500);
    const shots = path.join(process.cwd(), "e2e", "__screenshots__");
    fs.mkdirSync(shots, { recursive: true });
    await page.screenshot({ path: path.join(shots, `old-drive-map-${v.n}-dark.png`), fullPage: true });
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).exclude(".leaflet-container").analyze();
    const summary = res.violations.map((x) => `${x.id}: ${x.nodes.slice(0, 3).map((n) => n.target.join(" ")).join(" | ")}`);
    expect(summary, summary.join("\n")).toEqual([]);
    await ctx.close();
  });
}

test("a drive with no samples and no route says its figures are there, whatever its age", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  for (const id of ["old-bare", "new-bare"]) {
    await open(page, `/drives/${id}`);
    await expect(page.locator("[data-no-samples='figures']").first()).toContainText("Detailed data isn't available for this drive; its figures are.");
    await expect(page.getByText(/no longer kept|No telemetry was recorded/)).toHaveCount(0);
  }
  await ctx.close();
});

test("charges: no samples says the figures are there, old or new", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  for (const id of ["old-charge", "new-charge"]) {
    await open(page, `/charges/${id}`);
    await expect(page.locator("[data-no-samples='figures']").first()).toContainText("Detailed data isn't available for this charge; its figures are.");
  }
  await ctx.close();
});
