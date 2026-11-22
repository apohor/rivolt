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
  it("is driving = total minus charging, so the parts add up", () => {
    expect(timeSplit(14_700, 1_500)).toEqual({ driving: "3h 40m", charging: "25 min" });
  });
  it("is null without charging or with nonsense", () => {
    expect(timeSplit(14_700, 0)).toBeNull();
    expect(timeSplit(0, 100)).toBeNull();
    expect(timeSplit(1_000, 2_000)).toBeNull();
  });
});
