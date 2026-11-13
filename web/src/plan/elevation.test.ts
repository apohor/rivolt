import { describe, expect, it } from "vitest";
import { cumulative, heightAt, pointAtShare, shareOfPoint, stopShares, usableProfile, type LatLng } from "./elevation";

const PROFILE = { points: [[0, 100], [1000, 300], [3000, 100]] as [number, number][], waypoint_at_m: [0, 1000, 3000] };
// A line due east along the equator: 0.01 degrees is about 1112 m.
const LINE: LatLng[] = [[0, 0], [0, 0.01], [0, 0.03]];

describe("usableProfile", () => {
  it("accepts a forward series and rejects the rest", () => {
    expect(usableProfile(PROFILE)).toBe(PROFILE);
    expect(usableProfile(undefined)).toBeNull();
    expect(usableProfile({ points: [[0, 1]], waypoint_at_m: [] })).toBeNull();
    expect(usableProfile({ points: [[0, 1], [0, 2]], waypoint_at_m: [] })).toBeNull();
    expect(usableProfile({ points: [[0, 1], [5, 2], [3, 2]], waypoint_at_m: [] })).toBeNull();
    expect(usableProfile({ points: [[0, 1], [5, Number.NaN]], waypoint_at_m: [] })).toBeNull();
  });
});

describe("heightAt", () => {
  it("interpolates between points and clamps at the ends", () => {
    expect(heightAt(PROFILE, 0)).toBe(100);
    expect(heightAt(PROFILE, 1 / 6)).toBeCloseTo(200);
    expect(heightAt(PROFILE, 1 / 3)).toBeCloseTo(300);
    expect(heightAt(PROFILE, 1)).toBe(100);
    expect(heightAt(PROFILE, 2)).toBe(100);
    expect(heightAt(PROFILE, -1)).toBe(100);
  });
});

describe("stopShares", () => {
  it("skips the ends and unplaced stops", () => {
    expect(stopShares(PROFILE)).toEqual([{ index: 1, share: 1 / 3 }]);
    expect(stopShares({ ...PROFILE, waypoint_at_m: [0, null, 3000] })).toEqual([]);
  });
});

describe("the map line", () => {
  it("puts a share at the right place and reads it back", () => {
    const len = cumulative(LINE)[2];
    expect(len).toBeGreaterThan(3300);
    const mid = pointAtShare(LINE, 0.5)!;
    expect(mid[0]).toBeCloseTo(0, 6);
    expect(mid[1]).toBeCloseTo(0.015, 4);
    expect(pointAtShare(LINE, 0)).toEqual([0, 0]);
    expect(pointAtShare(LINE, 1)![1]).toBeCloseTo(0.03, 6);
    const hit = shareOfPoint(LINE, [0.0002, 0.02])!;
    expect(hit.share).toBeCloseTo(0.6667, 2);
    expect(hit.offMeters).toBeGreaterThan(20);
    expect(hit.offMeters).toBeLessThan(25);
  });
  it("has no answer for a line too short", () => {
    expect(pointAtShare([[1, 1]], 0.5)).toBeNull();
    expect(shareOfPoint([[1, 1], [1, 1]], [1, 1])).toBeNull();
  });
});
