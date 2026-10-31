import { describe, expect, it } from "vitest";
import { legColors, legStops, legsOf, splitIndexes } from "./legs";
import type { LatLng } from "./route";

// A made-up straight route near (10, 10): 101 vertices, about 0.1 mi apart in latitude.
const path: LatLng[] = Array.from({ length: 101 }, (_, i) => [10 + i * 0.0015, 10]);
const at = (i: number): LatLng => path[i];

describe("splitIndexes", () => {
  it("finds the path vertex nearest each stop, in order", () => {
    expect(splitIndexes(path, [at(30), at(70)])).toEqual([30, 70]);
  });
  it("never sends a later stop backwards, even if the route passes near an earlier stop again", () => {
    // A path that doubles back: the second stop is nearest an earlier vertex, but must come after the first.
    const loop: LatLng[] = [...path.slice(0, 51), ...path.slice(0, 50).reverse(), ...path.slice(0, 50)];
    const [a, b] = splitIndexes(loop, [at(40), at(20)]);
    expect(b).toBeGreaterThanOrEqual(a);
  });
});

describe("legsOf", () => {
  it("one leg when there is no stop between the ends", () => {
    const { legs, stops } = legsOf(path, [], "dark");
    expect(legs).toHaveLength(1);
    expect(stops).toHaveLength(4);
    expect(stops[0]).toBe(0);
    expect(stops[2]).toBe(1);
  });
  it("two stops make three legs whose lengths add up to the route", () => {
    const { legs } = legsOf(path, [at(30), at(70)], "light");
    expect(legs).toHaveLength(3);
    const total = legs.reduce((s, l) => s + l.miles, 0);
    // 100 steps of 0.0015 degrees of latitude is 10.4 miles.
    expect(total).toBeGreaterThan(10);
    expect(total).toBeLessThan(10.8);
    expect(legs[0].miles / total).toBeCloseTo(0.3, 1);
    expect(new Set(legs.map((l) => l.color)).size).toBe(3);
  });
  it("a stop at an end, or two on one vertex, adds no empty leg", () => {
    expect(legsOf(path, [at(0), at(100)], "dark").legs).toHaveLength(1);
    expect(legsOf(path, [at(50), at(50)], "dark").legs).toHaveLength(2);
  });
  it("the gradient steps hard at each stop: progress strictly increasing, each leg one colour", () => {
    const { legs, stops } = legsOf(path, [at(30), at(70)], "dark");
    const progress = stops.filter((_, i) => i % 2 === 0) as number[];
    for (let i = 1; i < progress.length; i++) expect(progress[i]).toBeGreaterThan(progress[i - 1]);
    expect(progress[0]).toBe(0);
    expect(progress[progress.length - 1]).toBe(1);
    expect(stops[1]).toBe(legs[0].color);
    expect(stops[3]).toBe(legs[0].color); // the first leg's colour holds to its end
    expect(stops[5]).toBe(legs[1].color);
    expect(stops[stops.length - 1]).toBe(legs[2].color);
  });
});

describe("legColors and legStops", () => {
  it("a lone leg takes the middle of the ramp, and distinct legs differ", () => {
    expect(legColors(1, "dark")).toHaveLength(1);
    const c = legColors(4, "dark");
    expect(new Set(c).size).toBe(4);
    expect(legColors(4, "dark")).not.toEqual(legColors(4, "light"));
  });
  it("a zero-length route still gives a valid two-stop expression", () => {
    expect(legStops([{ index: 0, miles: 0, color: "#123456" }], 0)).toEqual([0, "#123456", 1, "#123456"]);
  });
});
