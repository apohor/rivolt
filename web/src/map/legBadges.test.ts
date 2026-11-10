import { describe, expect, it } from "vitest";
import { drawLegBadges, legAnchor, type BadgesHandle, travelBearing, type LegWeatherInfo } from "./legBadges";
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

describe("drawLegBadges placement", () => {
  const info: LegWeatherInfo = { facts: { temp: "60 °F", tempShort: "60°", feels: null, wind: null, rain: false, snow: false }, title: "A to B", note: [] };
  // A map `width` px wide that projects [lng, lat] straight to pixels, and a Marker that records where it was put (only while it is on the map).
  const fake = (x: number, width = 350, legs = 1) => {
    const placed: { anchor: string; offset: [number, number] }[] = [];
    const map = { project: ([lng, lat]: [number, number]) => ({ x: lng, y: lat }), getContainer: () => ({ clientWidth: width }) };
    class Marker {
      constructor(private o: { anchor: string; offset: [number, number] }) {}
      setLngLat() { return this; }
      addTo() { placed.push(this.o); return this; }
      remove() { const i = placed.indexOf(this.o); if (i >= 0) placed.splice(i, 1); }
    }
    // `legs` legs on the same spot: the second one's badge would lie over the first's.
    const path: LatLng[] = [[100, x - 20], [100, x + 20]];
    const stops: LatLng[] = [[100, x - 20], [100, x + 20]];
    return { placed, run: (avoid: { x0: number; y0: number; x1: number; y1: number }[] = [], pins: { x0: number; y0: number; x1: number; y1: number }[] = [], prev?: BadgesHandle) => drawLegBadges({ Marker } as never, map as never, path, stops, Array(legs).fill(info), "dark", () => {}, avoid, pins, prev) };
  };

  it("keeps the badges it drew when drawn again with nothing moved, and replaces them when one would sit on the other side", () => {
    const a = fake(170);
    const first = a.run();
    expect(a.run([], [], first)).toBe(first);
    expect(a.placed).toHaveLength(1);
    const next = a.run(a.placed[0].anchor === "left" ? [{ x0: 170, y0: 80, x1: 340, y1: 120 }] : [{ x0: 0, y0: 80, x1: 170, y1: 120 }], [], first);
    expect(next).not.toBe(first);
    first();
    expect(a.placed).toHaveLength(1);
  });

  it("draws the badges again when the map flavor changes", () => {
    const a = fake(170);
    const first = a.run();
    expect(drawLegBadges({ Marker: class { constructor(private o: unknown) { void this.o; } setLngLat() { return this; } addTo() { return this; } remove() {} } } as never, { project: ([lng, lat]: [number, number]) => ({ x: lng, y: lat }), getContainer: () => ({ clientWidth: 350 }) } as never, [[100, 150], [100, 190]], [[100, 150], [100, 190]], [info], "light", () => {}, [], [], first)).not.toBe(first);
  });

  it("puts a badge on the other side of the line when an overlay is in its way", () => {
    const a = fake(170);
    a.run();
    const preferred = a.placed[0].anchor;
    const b = fake(170);
    b.run(preferred === "left" ? [{ x0: 170, y0: 80, x1: 340, y1: 120 }] : [{ x0: 0, y0: 80, x1: 170, y1: 120 }]);
    expect(b.placed).toHaveLength(1);
    expect(b.placed[0].anchor).not.toBe(preferred);
  });

  it("still shows a badge that no side clears of the overlays (the map is small), but not one over a pin or another badge", () => {
    const blocked = fake(170);
    blocked.run([{ x0: 0, y0: 80, x1: 350, y1: 120 }]);
    expect(blocked.placed).toHaveLength(1);
    const overPins = fake(170, 400);
    overPins.run([], [{ x0: 0, y0: 80, x1: 350, y1: 120 }]);
    expect(overPins.placed).toHaveLength(0);
    const tightMap = fake(170, 320);
    tightMap.run([], [{ x0: 0, y0: 80, x1: 350, y1: 120 }]);
    expect(tightMap.placed).toHaveLength(1);
    const two = fake(170, 350, 2);
    two.run();
    expect(two.placed).toHaveLength(1);
  });

  it("nudges a badge that would run past the map's edge back onto the map", () => {
    const edge = fake(20, 60);
    edge.run();
    expect(edge.placed).toHaveLength(1);
    expect(Math.abs(edge.placed[0].offset[0])).toBeGreaterThan(12);
  });
});
