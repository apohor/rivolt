import { describe, expect, it } from "vitest";
import { legAnchor, travelBearing } from "./legBadges";
import type { LatLng } from "./route";

describe("legAnchor", () => {
  const path: LatLng[] = [0, 1, 2, 3, 4, 5, 6, 7, 8].map((i) => [34 + i * 0.1, -118 + (i % 2 ? 0.05 : 0) + i * 0.1]);
  it("sits on the drawn route half way between the stops, heading the way you drive", () => {
    const { at, dir } = legAnchor(path, path[0], path[8]);
    expect(at).toEqual(path[4]);
    expect(dir[0]).toBeGreaterThan(0);
  });
  it("falls back to the straight midpoint with no drawn route", () => {
    expect(legAnchor([], [34, -118], [36, -116]).at).toEqual([35, -117]);
  });
});

describe("travelBearing", () => {
  it("is clockwise from north", () => {
    expect(Math.round(travelBearing([0, 0], [1, 0]))).toBe(0);
    expect(Math.round(travelBearing([0, 0], [0, 1]))).toBe(90);
    expect(Math.round(travelBearing([0, 0], [-1, 0]))).toBe(180);
  });
});
