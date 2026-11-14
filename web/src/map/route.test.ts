import { describe, expect, it } from "vitest";
import { boundsOf, clearRanges, colorAtProgress, gapProgress, routeData, setDriveGradient, setDriveRoute, speedColor, speedRuns, type LatLng } from "./route";
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

describe("setDriveRoute", () => {
  // A map stand-in that records sources, layers and paint changes.
  function fake() {
    const sources = new Map<string, { data: unknown; setData: (d: unknown) => void }>();
    const layers: { id: string; before?: string }[] = [];
    const paint = new Map<string, unknown>();
    return {
      sources, layers, paint,
      getSource: (id: string) => sources.get(id),
      addSource: (id: string, spec: { data: unknown; lineMetrics?: boolean }) => { const s = { data: spec.data, lineMetrics: spec.lineMetrics, setData: (d: unknown) => { s.data = d; } }; sources.set(id, s); },
      getLayer: (id: string) => (layers.some((l) => l.id === id) ? {} : undefined),
      addLayer: (l: { id: string }, before?: string) => { layers.push({ id: l.id, before }); },
      setPaintProperty: (id: string, p: string, v: unknown) => { paint.set(`${id}:${p}`, v); },
    };
  }
  const track: [number, number][] = [[10, 10], [10.1, 10.1], [10.2, 10.1]];
  const stops = [0, "#000000", 1, "#ffffff"];

  it("makes one metrics-enabled track and the gradient layers, under the first label", () => {
    const m = fake();
    setDriveRoute(m as never, { track, stops }, "dark", "roads_labels_major");
    const src = m.sources.get("rivolt-track") as unknown as { lineMetrics: boolean; data: { features: { geometry: { coordinates: number[][] } }[] } };
    expect(src.lineMetrics).toBe(true);
    expect(src.data.features).toHaveLength(1);
    expect(src.data.features[0].geometry.coordinates[0]).toEqual([10, 10]); // [lon, lat]
    expect(m.layers.map((l) => l.id)).toEqual(["rivolt-track-glow", "rivolt-track-casing", "rivolt-track-main", "rivolt-drive-gapcase", "rivolt-drive-routed", "rivolt-drive-gap"]);
    expect(m.layers.every((l) => l.before === "roads_labels_major")).toBe(true);
  });
  it("a second call replaces the data and regradients without adding layers", () => {
    const m = fake();
    setDriveRoute(m as never, { track, stops }, "light");
    setDriveRoute(m as never, { track: track.slice(0, 2), stops: [0, "#111111", 1, "#222222"] }, "light");
    expect(m.layers).toHaveLength(6);
    expect((m.sources.get("rivolt-track")!.data as { features: { geometry: { coordinates: unknown[] } }[] }).features[0].geometry.coordinates).toHaveLength(2);
    expect(JSON.stringify(m.paint.get("rivolt-track-main:line-gradient"))).toContain("#222222");
  });
  it("setDriveGradient recolours the glow and the line only", () => {
    const m = fake();
    setDriveRoute(m as never, { track, stops }, "dark");
    setDriveGradient(m as never, [0, "#aa0000", 1, "#00aa00"]);
    expect([...m.paint.keys()].sort()).toEqual(["rivolt-track-glow:line-gradient", "rivolt-track-main:line-gradient"]);
  });
  it("a route with fewer than two points draws no track", () => {
    const m = fake();
    setDriveRoute(m as never, { track: [[10, 10]], stops }, "dark");
    expect((m.sources.get("rivolt-track")!.data as { features: unknown[] }).features).toHaveLength(0);
  });
});

describe("gap_dim helpers", () => {
  const stops: (number | string)[] = [0, "#aaa", 0.5, "#bbb", 1, "#ccc"];

  it("colorAtProgress takes the last stop at or before p", () => {
    expect(colorAtProgress(stops, 0.2)).toBe("#aaa");
    expect(colorAtProgress(stops, 0.5)).toBe("#bbb");
    expect(colorAtProgress(stops, 1)).toBe("#ccc");
  });

  it("clearRanges blanks a range with hard edges and keeps stops ascending", () => {
    const out = clearRanges(stops, [[0.3, 0.4]]);
    const ps = out.filter((_, i) => i % 2 === 0).map(Number);
    expect([...ps].sort((a, b) => a - b)).toEqual(ps);
    expect(new Set(ps).size).toBe(ps.length);
    expect(out).toContain("rgba(0,0,0,0)");
    expect(out.slice(0, 2)).toEqual([0, "#aaa"]);
    expect(out.slice(-2)).toEqual([1, "#ccc"]);
  });

  it("clearRanges drops stops inside the range and passes through with no ranges", () => {
    expect(clearRanges(stops, [])).toBe(stops);
    expect(clearRanges(stops, [[0.4, 0.6]]).filter((_, i) => i % 2 === 0)).not.toContain(0.5);
  });

  it("gapProgress maps a dropout to the track's progress", () => {
    const track: [number, number][] = [[0, 0], [0, 1], [0, 2], [0, 3], [0, 4]];
    const [r] = gapProgress(track, [{ from: [0, 1], to: [0, 3] }]);
    expect(r[0]).toBeCloseTo(0.25, 2);
    expect(r[1]).toBeCloseTo(0.75, 2);
  });
});

describe("setDriveRoute with dimmed dropouts", () => {
  const track: LatLng[] = [[10, 10], [10.1, 10.1], [10.2, 10.2], [10.3, 10.3]];
  const stops = [0, "#2b7de9", 1, "#f0a000"];
  const gaps = [{ from: track[1], to: track[2] }];
  function fake() {
    const layers = new Map<string, { paint: Record<string, unknown>; filter?: unknown }>();
    const sources = new Map<string, { setData: (d: unknown) => void }>();
    return {
      layers,
      getSource: (id: string) => sources.get(id),
      addSource: (id: string) => { sources.set(id, { setData: () => {} }); },
      getLayer: (id: string) => (layers.has(id) ? {} : undefined),
      addLayer: (l: { id: string; paint: Record<string, unknown>; filter?: unknown }) => { layers.set(l.id, l); },
      removeLayer: (id: string) => { layers.delete(id); },
      setPaintProperty: () => {},
    };
  }

  it("draws a dropout solid, faded and in the route's colour, and clears the track under it", () => {
    const m = fake();
    setDriveRoute(m as never, { track, stops, gaps, gapRanges: gapProgress(track, gaps) }, "light");
    const gap = m.layers.get("rivolt-drive-gap")!.paint;
    expect(gap["line-dasharray"]).toBeUndefined();
    expect(gap["line-opacity"]).toBe(0.35);
    expect(gap["line-width"]).toBe(4.6);
    expect(JSON.stringify(m.layers.get("rivolt-track-main")!.paint["line-gradient"])).toContain("rgba(0,0,0,0)");
    expect(m.layers.get("rivolt-drive-gapcase")!.filter).toEqual(["==", ["get", "kind"], "routed"]);
  });

  it("rebuilds the dashed layers into dimmed ones when the dropouts arrive after the first draw", () => {
    const m = fake();
    setDriveRoute(m as never, { track, stops }, "light");
    expect(m.layers.get("rivolt-drive-gap")!.paint["line-dasharray"]).toEqual([3, 3]);
    setDriveRoute(m as never, { track, stops, gaps, gapRanges: gapProgress(track, gaps) }, "light");
    expect(m.layers.get("rivolt-drive-gap")!.paint["line-dasharray"]).toBeUndefined();
  });

  it("without the flag it keeps the dashed neutral dropout", () => {
    const m = fake();
    setDriveRoute(m as never, { track, stops, gaps }, "light");
    expect(m.layers.get("rivolt-drive-gap")!.paint["line-dasharray"]).toEqual([3, 3]);
  });
});

describe("gapProgress on a track that passes a place twice", () => {
  it("keeps the range to the dropout, not the stretch between two passes", () => {
    // Out along lon 0..4 and back along the same road (lon 4..0): a dropout between lon 3 and 4 on the way out.
    const track: [number, number][] = [[0, 0], [0, 1], [0, 2], [0, 3], [0, 4], [0, 3], [0, 2], [0, 1], [0, 0]];
    const [r] = gapProgress(track, [{ from: [0, 3], to: [0, 4] }]);
    expect(r[1] - r[0]).toBeCloseTo(0.125, 2);
    expect(r[0]).toBeCloseTo(0.375, 2);
  });

  it("finds a dropout on the way back by its own ends", () => {
    const track: [number, number][] = [[0, 0], [0, 1], [0, 2], [0, 3], [0, 4], [0, 3], [0, 2], [0, 1], [0, 0]];
    const rs = gapProgress(track, [{ from: [0, 3], to: [0, 4] }, { from: [0, 2], to: [0, 1] }]);
    expect(rs[0][1] - rs[0][0]).toBeLessThan(0.2);
    expect(rs[1][1] - rs[1][0]).toBeLessThan(0.2);
  });

  it("is not fooled by a fix a little nearer the other pass", () => {
    // The return pass runs ~6 m north of the way out; the dropout end is 1 m from it and 5 m from the way out.
    const out: [number, number][] = [[0, 0], [0, 0.01], [0, 0.02], [0, 0.03], [0, 0.04]];
    const back: [number, number][] = [[5.4e-5, 0.03], [5.4e-5, 0.02], [5.4e-5, 0.01], [5.4e-5, 0]];
    const [r] = gapProgress([...out, ...back], [{ from: [4.5e-5, 0.02], to: [0, 0.03] }]);
    expect(r[1] - r[0]).toBeLessThan(0.2);
  });
});
