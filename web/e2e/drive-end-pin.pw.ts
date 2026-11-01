import { expect, test } from "@playwright/test";
import { stubApi, VEHICLE_ID } from "./stub";
import { e2eNow } from "./clock";

// An ended drive shows both pins even when the parked frame just after it carries a stale position. The parked samples around a drive anchor its pins
// (they are more reliable than the stored endpoints), but a parked frame stamped with the truck's home, miles from where the drive ended, used to
// pin the end at home, on the start's own spot, and the map drew one pin and no "End" pill. Made-up places near (10, 10).
const MIN = 60_000;
const START = e2eNow() - 3 * 3_600_000;

test("iphone: a drive whose parked frame after it says home still shows its end pin", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: "dark" });
  const page = await ctx.newPage();
  await stubApi(page);
  const drive = {
    ID: "drive-1", VehicleID: VEHICLE_ID,
    StartedAt: new Date(START).toISOString(), EndedAt: new Date(START + 30 * MIN).toISOString(),
    StartSoCPct: 80, EndSoCPct: 70, StartOdometerMi: 5000, EndOdometerMi: 5020, DistanceMi: 20,
    StartLat: 10, StartLon: 10, EndLat: 10.192, EndLon: 10.192,
    MaxSpeedMph: 70, AvgSpeedMph: 40, EnergyUsedKWh: 8, Source: "live",
  };
  const row = (at: number, lat: number, lon: number, shift: string, i: number) => ({
    VehicleID: VEHICLE_ID, At: new Date(at).toISOString(),
    BatteryLevelPct: 80 - i / 12, RangeMi: 250, OdometerMi: 5000 + i / 6,
    Lat: lat, Lon: lon, SpeedMph: shift === "P" ? 0 : 30 + (i % 20),
    ShiftState: shift, ChargingState: "", ChargerPowerKW: 0, ChargeLimitPct: 80,
    InsideTempC: 21, OutsideTempC: 11, DriveNumber: 1, ChargeNumber: 0, Source: "live",
  });
  const samples = [
    row(START - 2 * MIN, 10, 10, "P", 0), // parked at the start, before the drive
    ...Array.from({ length: 121 }, (_, i) => row(START + i * 15_000, 10 + i * 0.0016, 10 + i * 0.0016, "D", i)),
    row(START + 30 * MIN + 30_000, 10, 10, "P", 121), // parked just after, but carrying the start's position: stale, 27 km from the drive's end
  ];
  await page.route("**/api/drives?**", (r) => r.fulfill({ json: [drive] }));
  await page.route("**/api/samples**", (r) => r.fulfill({ json: samples }));
  await page.goto("/drives/drive-1");
  await expect(page.locator("[data-map-drawn]:not([data-map-drawn='0'])").first()).toBeAttached();
  // Start and end: two pins, not one folded into the start.
  await expect(page.locator("[data-map-drawn] .maplibregl-marker")).toHaveCount(2);
  await ctx.close();
});
