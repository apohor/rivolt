import { describe, expect, it } from "vitest";
import { distanceKm, MAX_ANCHOR_KM, pickAnchor } from "./driveAnchor";

// Made-up places near (10, 10): a route's two ends, 20 km apart, and a "home" parked frame far from the end.
const routeStart = { lat: 10, lon: 10 };
const routeEnd = { lat: 10.18, lon: 10 }; // about 20 km north
const home = { lat: 10.001, lon: 10.001 }; // right by the start

describe("pickAnchor", () => {
  it("keeps a parked sample that is where the route ends", () => {
    const parked = { lat: 10.1801, lon: 10.0001 };
    expect(pickAnchor(parked, routeEnd, { lat: 0, lon: 0 })).toBe(parked);
  });
  it("drops a parked frame carrying the wrong place (home stamped on a drive that ended 20 km away) and uses the stored end", () => {
    const stored = { lat: 10.18, lon: 10.0002 };
    expect(pickAnchor(home, routeEnd, stored)).toBe(stored);
  });
  it("uses the stored endpoint when there is no parked sample, and trusts the anchor when there is no route to compare", () => {
    const stored = { lat: 10, lon: 10 };
    expect(pickAnchor(undefined, routeEnd, stored)).toBe(stored);
    expect(pickAnchor(home, undefined, stored)).toBe(home);
  });
  it("the cutoff is MAX_ANCHOR_KM", () => {
    const justIn = { lat: routeStart.lat + (MAX_ANCHOR_KM - 0.1) / 111.2, lon: routeStart.lon };
    const justOut = { lat: routeStart.lat + (MAX_ANCHOR_KM + 0.1) / 111.2, lon: routeStart.lon };
    expect(pickAnchor(justIn, routeStart, routeEnd)).toBe(justIn);
    expect(pickAnchor(justOut, routeStart, routeEnd)).toBe(routeEnd);
  });
});

describe("distanceKm", () => {
  it("is about 111 km a degree of latitude", () => {
    expect(distanceKm({ lat: 10, lon: 10 }, { lat: 11, lon: 10 })).toBeCloseTo(111.2, 0);
  });
});
