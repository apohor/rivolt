import { describe, expect, it } from "vitest";
import type { WhatIfResponse, WhatIfStop } from "../lib/api";
import { insertionIndex, signedDuration, whatIfFailureLine, whatIfLines, WHAT_IF_NOTICE } from "./whatIf";

const stop = (lat: number, lon: number, source: WhatIfStop["source"]): WhatIfStop => ({ lat, lon, source });

describe("what-if preview", () => {
  const line: [number, number][] = [[0, 0], [0, 1], [0, 2], [0, 3], [0, 4]];
  const stops = [stop(0, 0, "origin"), stop(0, 2, "planner"), stop(0, 4, "destination")];
  it("inserts a new stop before the first stop the line reaches after it", () => {
    expect(insertionIndex(stops, { lat: 0.1, lon: 1 }, line)).toBe(1);
    expect(insertionIndex(stops, { lat: 0.1, lon: 3 }, line)).toBe(2);
    expect(insertionIndex(stops, { lat: 0, lon: 4 }, line)).toBe(2);
    expect(insertionIndex(stops, { lat: 0, lon: 1 }, [])).toBe(2);
  });
  it("formats signed durations", () => {
    expect(signedDuration(780)).toBe("+13 min");
    expect(signedDuration(-300)).toBe("−5 min");
    expect(signedDuration(3900)).toBe("+1 h 5 min");
    expect(signedDuration(undefined)).toBeNull();
  });
  const base: WhatIfResponse = { estimate: true, estimated: false, notice: "estimate_replan", segment: { from: 1, to: 2 }, added_m: 12400, added_sec: 780, arrival_shift_sec: 780 };
  it("leads with the change, ends with the estimate notice, battery only as a change", () => {
    const l = whatIfLines({ ...base, next_charge: { index: 2, is_destination: false, soc_drop_pts: 4.1 }, warnings: ["may_not_reach_next_charge"] }, "mi");
    expect(l[0]).toBe("+13 min · +7.7 mi");
    expect(l).toContain("About 4 points lower at the next charger");
    expect(l).toContain("May not reach the next charger (estimate)");
    expect(l[l.length - 1]).toBe(WHAT_IF_NOTICE);
    expect(l.join(" ")).not.toMatch(/free|working|stall/i);
  });
  it("says 1 point, not 1 points", () => {
    const l = whatIfLines({ ...base, next_charge: { index: 2, is_destination: true, soc_drop_pts: 1.2 } }, "mi");
    expect(l).toContain("About 1 point lower at the destination");
  });
  it("says rough when the router was unavailable, and nothing without numbers", () => {
    expect(whatIfLines({ ...base, estimated: true }, "km").some((x) => x.startsWith("Rough estimate"))).toBe(true);
    expect(whatIfLines({ ...base, added_sec: NaN, added_m: NaN }, "km")).toEqual([]);
  });
  it("404 shows nothing and 429 says busy", () => {
    expect(whatIfFailureLine(404)).toBeNull();
    expect(whatIfFailureLine(429)).toContain("busy");
  });
});
