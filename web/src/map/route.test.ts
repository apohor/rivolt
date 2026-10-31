import { describe, expect, it } from "vitest";
import { boundsOf, routeData, speedColor, speedRuns, type LatLng } from "./route";
import { SPEED_BUCKETS } from "../lib/speedBuckets";

const line: LatLng[] = [[40, -100], [40.1, -100.1], [40.2, -100.2], [40.3, -100.3]];

describe("speedRuns", () => {
  it("is one uniform run without speeds, in [lon, lat] order", () => {
    const r = speedRuns(line);
    expect(r).toHaveLength(1);
    expect(r[0].geometry.coordinates[0]).toEqual([-100, 40]);
    expect(r[0].properties.color).toBe(speedColor(undefined));
  });

  it("cuts a run where the speed band changes, sharing the end point so the line stays continuous", () => {
    const r = speedRuns(line, [3, 3, 60, 60]);
    expect(r).toHaveLength(2);
    expect(r[0].properties.color).toBe(SPEED_BUCKETS[0].color);
    expect(r[1].properties.color).toBe(SPEED_BUCKETS[3].color);
    expect(r[0].geometry.coordinates.at(-1)).toEqual(r[1].geometry.coordinates[0]);
    expect(r[0].geometry.coordinates.length + r[1].geometry.coordinates.length).toBe(line.length + 1);
  });

  it("needs two points, and ignores a speed list of the wrong length", () => {
    expect(speedRuns([[40, -100]])).toEqual([]);
    expect(speedRuns(line, [10, 10])).toHaveLength(1);
  });
});

describe("routeData and boundsOf", () => {
  it("adds routed gap-fills and dashed connectors as their own kinds", () => {
    const d = routeData({ runs: speedRuns(line), routedGaps: [[[40, -100], [40.05, -100.05]], [[1, 1]]], gaps: [{ from: [40, -100], to: [40.3, -100.3] }] });
    expect(d.features.map((f) => f.properties.kind)).toEqual(["speed", "routed", "gap"]);
  });

  it("bounds ignore non-finite points and are null for none", () => {
    expect(boundsOf([[40, -100], [41, -99], [NaN, 5]])).toEqual([[-100, 40], [-99, 41]]);
    expect(boundsOf([[NaN, NaN]])).toBeNull();
    expect(boundsOf([])).toBeNull();
  });
});
