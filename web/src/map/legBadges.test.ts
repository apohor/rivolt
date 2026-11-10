import { describe, expect, it } from "vitest";
import { drawLegBadges, legAnchor, travelBearing, type LegWeatherInfo } from "./legBadges";
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
  // A map 350 px wide that projects [lng, lat] straight to pixels, and a Marker that records where it was put.
  const fake = (x: number, width = 350) => {
    const placed: { anchor: string }[] = [];
    const map = { project: ([lng, lat]: [number, number]) => ({ x: lng, y: lat }), getContainer: () => ({ clientWidth: width }) };
    class Marker {
      constructor(private o: { anchor: string }) {}
      setLngLat() { return this; }
      addTo() { placed.push(this.o); return this; }
      remove() { placed.splice(placed.indexOf(this.o), 1); }
    }
    const path: LatLng[] = [[100, x - 20], [100, x + 20]];
    const stops: LatLng[] = [[100, x - 20], [100, x + 20]];
    return { placed, run: (avoid: { x0: number; y0: number; x1: number; y1: number }[] = []) => drawLegBadges({ Marker } as never, map as never, path, stops, [info], "dark", () => {}, avoid) };
  };

  it("puts a badge on the other side of the line when a pin is in its way", () => {
    const a = fake(170);
    a.run();
    const preferred = a.placed[0].anchor;
    const b = fake(170);
    // Block the side the badge would take: a box over the right side (or the left) of the point.
    b.run(preferred === "left" ? [{ x0: 170, y0: 80, x1: 340, y1: 120 }] : [{ x0: 0, y0: 80, x1: 170, y1: 120 }]);
    expect(b.placed).toHaveLength(1);
    expect(b.placed[0].anchor).not.toBe(preferred);
  });

  it("leaves a badge out when neither side is clear, or when it would run past the edge of the map", () => {
    const blocked = fake(170);
    blocked.run([{ x0: 0, y0: 80, x1: 350, y1: 120 }]);
    expect(blocked.placed).toHaveLength(0);
    const edge = fake(20, 60);
    edge.run();
    expect(edge.placed).toHaveLength(0);
  });
});
