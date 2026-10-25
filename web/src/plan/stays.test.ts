import { describe, expect, it } from "vitest";
import { campgroundPageUrl, campPowerLine, hookupFromAmps, sitesLine, RIDB_CREDIT_TEXT, offTargetLine, searchedLine, fastChargerLine, httpsOr, nearestLines, withNight } from "./stays";

describe("campground helpers", () => {
  it("links only to a campground page by numeric id", () => {
    expect(campgroundPageUrl("https://www.recreation.gov/camping/campgrounds/232447")).toBe("https://www.recreation.gov/camping/campgrounds/232447");
    expect(campgroundPageUrl("https://www.recreation.gov/camping/campgrounds/232447?x=1")).toBe("");
    expect(campgroundPageUrl("javascript:alert(1)")).toBe("");
    expect(campgroundPageUrl("https://evil.example/camping/campgrounds/1")).toBe("");
    expect(campgroundPageUrl(undefined, "ridb/232447")).toBe("https://www.recreation.gov/camping/campgrounds/232447");
    expect(campgroundPageUrl(undefined, "ridb/<b>")).toBe("");
  });
  it("maps amps to a hookup, and hides 15-20 A", () => {
    expect(hookupFromAmps(50)).toEqual({ hookup: "nema_14_50", kw: 7.2 });
    expect(hookupFromAmps(30)).toEqual({ hookup: "tt_30", kw: 1.4 });
    expect(hookupFromAmps(20)).toBeNull();
    expect(hookupFromAmps(undefined)).toBeNull();
  });
  it("power lines are estimates and name the Portable Charger", () => {
    expect(campPowerLine("nema_14_50", 7.2)).toBe("About 7.2 kW (estimate) on a 50 A site with Rivian's Portable Charger.");
    expect(campPowerLine("tt_30", 1.4)).toContain("needs an adapter not included with the truck");
    expect(campPowerLine("j1772", 7)).toBe("The station's power varies; planned at 7 kW (estimate).");
  });
  it("sites line needs both counts", () => {
    expect(sitesLine(12, 40)).toBe("12 of 40 sites have power");
    expect(sitesLine(undefined, 40)).toBe("");
  });
  it("credit text", () => {
    expect(RIDB_CREDIT_TEXT).toBe("Data: Recreation.gov (RIDB), CC BY 4.0; filtered by Rivolt");
  });
});

describe("planner search lines", () => {
  const fmt = (km: number) => `${Math.round(km * 0.621371)} mi`;
  const route = { route_m: 900_000, drive_sec: 36_000 }; // 25 m/s
  it("says how far the empty search looked on each side", () => {
    expect(searchedLine({ window_before_min: 180, window_after_min: 60 }, route, 8, fmt)).toBe(
      "No hotel or campground with Level 2 or a hookup within about 168 mi before or 56 mi after your 8 h point. Pick a stop by hand.",
    );
  });
  it("falls back to the plain line on an older server", () => {
    expect(searchedLine({}, route, 8, fmt)).toContain("found near here");
  });
  it("names a stay that is not near the point", () => {
    expect(offTargetLine(400_000, 360_000, 8, fmt)).toBe("about 25 mi before your 8 h point");
    expect(offTargetLine(400_000, 440_000, 8, fmt)).toBe("about 25 mi after your 8 h point");
    expect(offTargetLine(400_000, 395_000, 8, fmt)).toBe("");
    expect(offTargetLine(400_000, undefined, 8, fmt)).toBe("");
  });
});

describe("hotels with no charger and the nearest listed stay", () => {
  const fmt = (km: number) => `${Math.round(km * 0.621371)} mi`;
  it("names the nearest listed stay on each side, in hours", () => {
    expect(
      nearestLines({
        before: { name: "Sample Inn West", drive_sec: 18_000, offset_sec: -11_880 },
        after: { name: "Sample Inn East", drive_sec: 35_000, offset_sec: 5_760 },
      }),
    ).toEqual([
      "Nearest listed stay: Sample Inn West, about 3.3 h short of the day (about 5.0 h in).",
      "Nearest listed stay: Sample Inn East, about 1.6 h past the day (about 9.7 h in).",
    ]);
    expect(nearestLines(undefined)).toEqual([]);
  });
  it("says a hotel has no charger and names the morning's fast charger", () => {
    expect(fastChargerLine({ name: "Sample Fast Charge A", distance_m: 3200, max_power_kw: 250 }, fmt)).toBe(
      "No charger listed here. Nearest fast charger: Sample Fast Charge A, 2 mi away (250 kW).",
    );
    expect(fastChargerLine(undefined, fmt)).toBe("No charger listed here.");
    expect(fastChargerLine({ name: "Sample Fast Charge A", distance_m: 0 }, fmt)).toBe("No charger listed here. Nearest fast charger: Sample Fast Charge A, right next to it.");
    expect(fastChargerLine({ name: "Sample Fast Charge A", distance_m: 3200 }, fmt)).toBe("No charger listed here. Nearest fast charger: Sample Fast Charge A, 2 mi away.");
  });
  it("makes a place the night, and the fast charger a plain stop after it", () => {
    const night = { lat: 43.5, lon: -120.5, label: "Sample Motel" };
    const via = { lat: 43.51, lon: -120.49, label: "Sample Fast Charge A" };
    const r = withNight([{ lat: 44, lon: -121, label: "Via" }], [false], night, via);
    expect(r.stops.map((s) => s.label)).toEqual(["Via", "Sample Motel", "Sample Fast Charge A"]);
    expect(r.flags).toEqual([false, true, false]);
  });
  it("turns a stop already on the trip into the night, and never adds the charger twice", () => {
    const stops = [{ lat: 43.5, lon: -120.5, label: "Sample Motel" }, { lat: 43.51, lon: -120.49, label: "Sample Fast Charge A" }];
    const r = withNight(stops, [false, false], { lat: 43.5002, lon: -120.5001, label: "Sample Motel" }, stops[1]);
    expect(r.stops).toHaveLength(2);
    expect(r.flags).toEqual([true, false]);
  });
  it("leaves a plain night as it was", () => {
    const r = withNight([], [], { lat: 45.9, lon: -121, label: "Sample Inn" });
    expect(r).toEqual({ stops: [{ lat: 45.9, lon: -121, label: "Sample Inn" }], flags: [true] });
  });
});

describe("links from a response", () => {
  it("are used only when they are https, else the fallback", () => {
    expect(httpsOr("https://www.openstreetmap.org/copyright")).toBe("https://www.openstreetmap.org/copyright");
    expect(httpsOr("javascript:alert(1)", "https://opendatacommons.org/licenses/odbl/1-0/")).toBe("https://opendatacommons.org/licenses/odbl/1-0/");
    expect(httpsOr("http://example.test/x")).toBeUndefined();
    expect(httpsOr(undefined, "https://a.test")).toBe("https://a.test");
  });
});
