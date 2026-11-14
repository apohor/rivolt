import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";
import { DRAWN_MS, drawnMap } from "./drawn";

// A round trip of three drives and a zero-mile stub (the truck shifted at a stop and did not move) is one row of three legs: the stub is no leg, the
// page lists each leg with its parked time, marks each stop on the map, and draws every leg's route. Made-up places near (10, 10).
const MIN = 60_000;
const START = e2eNow() - 5 * 3_600_000;
const P: [number, number][] = [[10, 10], [10.1, 10.1], [10.1, 10.25], [10, 10]];

const leg = (id: string, from: number, mins: number, a: [number, number], b: [number, number], mi: number) => ({
  ID: id, VehicleID: VEHICLE_ID,
  StartedAt: new Date(from).toISOString(), EndedAt: new Date(from + mins * MIN).toISOString(),
  StartSoCPct: 80, EndSoCPct: 74, StartOdometerMi: 5000, EndOdometerMi: 5000 + mi, DistanceMi: mi,
  StartLat: a[0], StartLon: a[1], EndLat: b[0], EndLon: b[1],
  MaxSpeedMph: 60, AvgSpeedMph: 36, EnergyUsedKWh: mi / 3, Source: "live",
});
const sampleRow = (at: number, lat: number, lon: number, i: number) => ({
  VehicleID: VEHICLE_ID, At: new Date(at).toISOString(),
  BatteryLevelPct: 80 - i / 40, RangeMi: 250, OdometerMi: 5000 + i / 6,
  Lat: lat, Lon: lon, SpeedMph: 30 + (i % 20),
  ShiftState: "D", ChargingState: "", ChargerPowerKW: 0, ChargeLimitPct: 80,
  InsideTempC: 21, OutsideTempC: 11, DriveNumber: 1, ChargeNumber: 0, Source: "live",
});

for (const [name, viewport, scheme] of [
  ["iphone-dark", { width: 393, height: 852 }, "dark"],
  ["desktop-light", { width: 1280, height: 900 }, "light"],
] as const) {
  test(`${name}: a three-leg round trip lists its legs, marks its stops and drops the stub`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport, colorScheme: scheme });
    const page = await ctx.newPage();
    await stubApi(page);
    const starts = [START, START + 40 * MIN, START + 75 * MIN];
    const legs = [
      leg("leg-1", starts[0], 20, P[0], P[1], 12),
      leg("leg-2", starts[1], 20, P[1], P[2], 9),
      leg("leg-3", starts[2], 25, P[2], P[3], 15),
    ];
    const stub = leg("stub", START + 30 * MIN, 1, P[1], P[1], 0);
    const samples = legs.flatMap((l, k) =>
      Array.from({ length: 61 }, (_, i) => {
        const f = i / 60;
        return sampleRow(starts[k] + f * 20 * MIN, P[k][0] + (P[k + 1][0] - P[k][0]) * f, P[k][1] + (P[k + 1][1] - P[k][1]) * f, i + k * 61);
      }),
    );
    await page.route("**/api/drives?**", (r) => r.fulfill({ json: [legs[2], legs[1], stub, legs[0]] }));
    await page.route("**/api/samples**", (r) => r.fulfill({ json: samples }));
    await page.goto("/drives/leg-1");
    await expect(drawnMap(page)).toBeAttached({ timeout: DRAWN_MS });
    const list = page.getByTestId("round-trip-legs");
    await expect(list.locator("li")).toHaveCount(3);
    await expect(list.locator("li").nth(0)).toContainText("Parked 20 min");
    await expect(list.locator("li").nth(1)).toContainText("Parked 15 min");
    await expect(list.locator("li").nth(2)).not.toContainText("Parked");
    await expect(page.locator("[data-map-drawn] [data-stop]")).toHaveCount(2);
    await page.getByText("Route", { exact: true }).first().scrollIntoViewIfNeeded();
    await page.screenshot({ path: `test-results/round-trip-legs-${name}-route.png` });
    await list.scrollIntoViewIfNeeded();
    await page.screenshot({ path: `test-results/round-trip-legs-${name}-legs.png` });
    await ctx.close();
  });
}
