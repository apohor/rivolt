import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";
import { DRAWN_MS, drawnMap } from "./drawn";

// A round trip of three legs and a zero-distance stub (made-up places near (10, 10)): home -> A (7 min), a 1-minute creep at A, A -> B, B -> home.
// Activity says "3 drives", not 4; the detail draws all three legs, pins each stop with its parked minutes, lists the legs and sums the figures.
const MIN = 60_000;
const START = e2eNow() - 4 * 3_600_000;
const HOME: [number, number] = [10, 10];
const A: [number, number] = [10.06, 10.06];
const B: [number, number] = [10.12, 10.02];

const drive = (id: string, from: number, mins: number, a: [number, number], b: [number, number], mi: number) => ({
  ID: id, VehicleID: VEHICLE_ID,
  StartedAt: new Date(START + from * MIN).toISOString(), EndedAt: new Date(START + (from + mins) * MIN).toISOString(),
  StartSoCPct: 80, EndSoCPct: 77, StartOdometerMi: 5000, EndOdometerMi: 5000 + mi, DistanceMi: mi,
  StartLat: a[0], StartLon: a[1], EndLat: b[0], EndLon: b[1],
  MaxSpeedMph: 55, AvgSpeedMph: 30, EnergyUsedKWh: mi > 0 ? 1.2 : 0, Source: "live",
});
const l1 = drive("leg-1", 0, 7, HOME, A, 3.1);
const stub = drive("leg-stub", 13, 1, A, A, 0);
const l2 = drive("leg-2", 20, 10, A, B, 3.3);
const l3 = drive("leg-3", 49, 7, B, HOME, 3.3);

// Samples for each leg: a straight line between its ends, parked frames at the stops carrying the stop's position.
const sample = (at: number, lat: number, lon: number, shift: string, i: number) => ({
  VehicleID: VEHICLE_ID, At: new Date(at).toISOString(),
  BatteryLevelPct: 80 - i / 40, RangeMi: 250, OdometerMi: 5000 + i / 10,
  Lat: lat, Lon: lon, SpeedMph: shift === "P" ? 0 : 25 + (i % 20),
  ShiftState: shift, ChargingState: "", ChargerPowerKW: 0, ChargeLimitPct: 80,
  InsideTempC: 21, OutsideTempC: 11, DriveNumber: 1, ChargeNumber: 0, Source: "live",
});
const along = (d: { StartedAt: string; EndedAt: string }, a: [number, number], b: [number, number]) => {
  const s = Date.parse(d.StartedAt);
  const e = Date.parse(d.EndedAt);
  const n = Math.round((e - s) / 15_000);
  return Array.from({ length: n + 1 }, (_, i) => sample(s + i * 15_000, a[0] + ((b[0] - a[0]) * i) / n, a[1] + ((b[1] - a[1]) * i) / n, "D", i));
};
const SAMPLES = [
  ...along(l1, HOME, A), sample(Date.parse(l1.EndedAt) + 60_000, A[0], A[1], "P", 0),
  ...along(l2, A, B), sample(Date.parse(l2.EndedAt) + 60_000, B[0], B[1], "P", 0),
  ...along(l3, B, HOME), sample(Date.parse(l3.EndedAt) + 60_000, HOME[0], HOME[1], "P", 0),
];

for (const [w, h, scheme] of [[393, 852, "dark"], [393, 852, "light"], [1280, 900, "dark"], [1280, 900, "light"]] as const) {
  test(`round trip with a stub shows every leg (${w}, ${scheme})`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
    const page = await ctx.newPage();
    await stubApi(page);
    const asked: string[] = [];
    await page.route("**/api/drives?**", (r) => r.fulfill({ json: [l3, l2, stub, l1] }));
    await page.route("**/api/samples**", (r) => {
      const u = new URL(r.request().url());
      asked.push(u.searchParams.get("since") ?? "");
      const since = Date.parse(u.searchParams.get("since") ?? "") || 0;
      const until = Date.parse(u.searchParams.get("until") ?? "") || Infinity;
      return r.fulfill({ json: SAMPLES.filter((s) => Date.parse(s.At) >= since && Date.parse(s.At) <= until) });
    });

    // Activity: the stub is not a leg.
    await page.goto("/drives");
    await expect(page.locator("[data-round-trip-legs]").first()).toContainText("round trip, 3 drives");

    await page.goto("/drives/leg-1");
    await expect(page.getByRole("heading", { name: "Round trip" })).toBeVisible();
    // Summed figures: 3.1 + 3.3 + 3.3.
    await expect(page.getByText("9.7 mi").first()).toBeVisible();
    // The legs, in order, with the parked time after each but the last.
    await expect(page.locator("[data-leg]")).toHaveCount(3);
    await expect(page.locator("[data-leg-parked]")).toHaveText(["Parked 13 min", "Parked 19 min"]);
    // One request per leg, not one for the whole span.
    await expect.poll(() => asked.length).toBe(3);
    // All three legs and both stops are on the map: the shared start/end pin plus the two stops.
    await expect(drawnMap(page)).toBeAttached({ timeout: DRAWN_MS });
    await expect(page.locator("[data-map-drawn] .maplibregl-marker")).toHaveCount(3);
    await expect(page.locator("[data-stop]")).toHaveCount(2);
    await expect(page.locator("[data-stop]").first()).toHaveAttribute("data-stop", "Parked 13 min");
    await page.screenshot({ path: `${process.env.SHOTS_DIR ?? "test-results"}/round-trip-${w}-${scheme}.png`, fullPage: true });
    await ctx.close();
  });
}

function encode5(pts: [number, number][]): string {
  let pl = 0;
  let po = 0;
  let out = "";
  const enc = (v: number) => {
    let n = v < 0 ? ~(v << 1) : v << 1;
    let r = "";
    while (n >= 0x20) {
      r += String.fromCharCode((0x20 | (n & 0x1f)) + 63);
      n >>= 5;
    }
    return r + String.fromCharCode(n + 63);
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

// Samples gone (an import, or aged out): the map falls back to the stored routes, and it must draw every leg's, not only the first's.
test("round trip with no samples draws every leg's stored route and pins the stops", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 393, height: 852 }, colorScheme: "light" });
  const page = await ctx.newPage();
  await stubApi(page);
  const routed = [
    { ...l1, RoutePolyline: encode5([HOME, A]) },
    { ...l2, RoutePolyline: encode5([A, B]) },
    { ...l3, RoutePolyline: encode5([B, HOME]) },
  ];
  const fetched: string[] = [];
  await page.route("**/api/drives?**", (r) => r.fulfill({ json: [l3, l2, stub, l1] }));
  await page.route(/\/api\/drives\/[^/?]+$/, (r) => {
    const id = new URL(r.request().url()).pathname.split("/").pop();
    fetched.push(id ?? "");
    const d = routed.find((x) => x.ID === id);
    return d ? r.fulfill({ json: d }) : r.fulfill({ status: 404, json: {} });
  });
  await page.route("**/api/samples**", (r) => r.fulfill({ json: [] }));
  await page.goto("/drives/leg-1");
  await expect(page.getByRole("heading", { name: "Round trip" })).toBeVisible();
  await expect(page.locator(".maplibregl-map").first()).toBeVisible();
  expect(fetched).toEqual(expect.arrayContaining(["leg-1", "leg-2", "leg-3"]));
  await expect(page.locator("[data-stop]")).toHaveCount(2);
  await ctx.close();
});
