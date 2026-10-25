import { describe, expect, it } from "vitest";
import { SPEED_BUCKETS, speedBucketIndex, usedSpeedBuckets } from "./speedBuckets";

describe("speed bands", () => {
  it("name a band by its lower bound", () => {
    expect(SPEED_BUCKETS.map((b) => b.label)).toEqual(["<5", "5", "25", "50", "65+"]);
  });
  it("put a speed in the band it falls in, the lower bound belonging to the upper band", () => {
    expect([0, 4.9, 5, 24.9, 25, 50, 65, 120].map(speedBucketIndex)).toEqual([0, 0, 1, 1, 2, 3, 4, 4]);
  });
  it("show only the bands a drive uses, slowest first, and none for unknown speeds", () => {
    expect(usedSpeedBuckets([70, 30, undefined, 33, null, Number.NaN]).map((b) => b.label)).toEqual(["25", "65+"]);
    expect(usedSpeedBuckets([undefined, null])).toEqual([]);
  });
});
