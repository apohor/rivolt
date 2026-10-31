import { describe, expect, it } from "vitest";
import { boundsOf, routeData, setDriveGradient, setDriveRoute, speedColor, speedRuns, type LatLng } from "./route";
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
