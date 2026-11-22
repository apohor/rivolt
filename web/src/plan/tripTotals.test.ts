import { describe, expect, it } from "vitest";
import { timeSplit, tripTotalsText } from "./tripTotals";

describe("tripTotalsText", () => {
  it("writes distance, stops and the total time in the strip's form", () => {
    expect(tripTotalsText({ meters: 367_000, stops: 2, seconds: 14_700, unit: "mi" })).toBe("228 mi · 2 charging stops · 4h 5m");
    expect(tripTotalsText({ day: "Day 2", meters: 367_000, stops: 1, seconds: 10_800, unit: "mi" })).toBe("Day 2 · 228 mi · 1 charging stop · 3h");
    expect(tripTotalsText({ meters: 80_000, stops: 0, seconds: 3_000, unit: "km" })).toBe("80 km · 50 min");
  });
});

describe("timeSplit", () => {
  it("shows the plan's own driving and charging time", () => {
    expect(timeSplit(14_700, 1_500, 13_200)).toEqual({ driving: "3h 40m", charging: "25 min", other: null });
  });
  it("names what the total holds beyond them instead of calling it driving", () => {
    // A day with a 40 min rest: total 4h 45m, driving 3h 40m, charging 25 min.
    expect(timeSplit(17_100, 1_500, 13_200)).toEqual({ driving: "3h 40m", charging: "25 min", other: "40 min" });
  });
  it("treats a gap under 5 minutes as rounding", () => {
    expect(timeSplit(14_900, 1_500, 13_200)?.other).toBeNull();
  });
  it("is null when a part is unknown or the parts overrun the total", () => {
    expect(timeSplit(14_700, 0, 13_200)).toBeNull();
    expect(timeSplit(14_700, 1_500, 0)).toBeNull();
    expect(timeSplit(0, 100, 100)).toBeNull();
    expect(timeSplit(10_000, 5_000, 9_000)).toBeNull();
  });
});
