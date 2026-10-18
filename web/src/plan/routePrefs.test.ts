import { describe, expect, it } from "vitest";
import { anyRoutePref, avoidSummary, noRoutePrefs, routePrefsFromInputs, routePrefsWire } from "./routePrefs";

describe("routePrefsWire", () => {
  it("sends nothing when nothing is avoided, so the request is unchanged", () => {
    expect(routePrefsWire(noRoutePrefs)).toBeUndefined();
  });

  it("sends all three flags once any is on", () => {
    expect(routePrefsWire({ ...noRoutePrefs, avoidTolls: true })).toEqual({
      avoid_tolls: true,
      avoid_ferries: false,
      avoid_highways: false,
    });
    expect(routePrefsWire({ avoidTolls: false, avoidFerries: true, avoidHighways: true })).toEqual({
      avoid_tolls: false,
      avoid_ferries: true,
      avoid_highways: true,
    });
  });
});

describe("routePrefsFromInputs", () => {
  it("reads what was saved", () => {
    expect(routePrefsFromInputs({ route_preferences: { avoid_tolls: true, avoid_ferries: false, avoid_highways: true } })).toEqual({
      avoidTolls: true,
      avoidFerries: false,
      avoidHighways: true,
    });
  });

  it("treats anything missing or malformed as no preferences", () => {
    for (const bad of [undefined, null, 7, "x", [], {}, { route_preferences: null }, { route_preferences: "tolls" }, { route_preferences: { avoid_tolls: "yes" } }]) {
      expect(routePrefsFromInputs(bad)).toEqual(noRoutePrefs);
    }
  });

  it("round-trips through the wire shape", () => {
    const p = { avoidTolls: true, avoidFerries: true, avoidHighways: false };
    expect(routePrefsFromInputs({ route_preferences: routePrefsWire(p) })).toEqual(p);
  });
});

describe("anyRoutePref and avoidSummary", () => {
  it("knows when something is avoided", () => {
    expect(anyRoutePref(noRoutePrefs)).toBe(false);
    expect(anyRoutePref({ ...noRoutePrefs, avoidFerries: true })).toBe(true);
  });

  it("names what is avoided", () => {
    expect(avoidSummary(noRoutePrefs)).toBe("");
    expect(avoidSummary({ ...noRoutePrefs, avoidHighways: true })).toBe("highways");
    expect(avoidSummary({ avoidTolls: true, avoidFerries: false, avoidHighways: true })).toBe("tolls and highways");
    expect(avoidSummary({ avoidTolls: true, avoidFerries: true, avoidHighways: true })).toBe("tolls, ferries and highways");
  });
});
