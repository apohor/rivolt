import { describe, expect, it } from "vitest";
import { campgroundPageUrl, campPowerLine, hookupFromAmps, sitesLine, RIDB_CREDIT_TEXT, offTargetLine, searchedLine } from "./stays";

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
