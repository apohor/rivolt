import { describe, expect, it } from "vitest";
import { thinPath } from "./thinPath";

function route(n: number): [number, number][] {
  return Array.from({ length: n }, (_, i) => {
    const f = i / (n - 1);
    return [33 + 6 * f + 0.5 * Math.sin(f * 40), -97 - 8 * f] as [number, number];
  });
}

describe("thinPath", () => {
  it("thins a long route to the limit and keeps both ends", () => {
    const p = route(30000);
    const t = thinPath(p, 4000);
    expect(t.length).toBeLessThanOrEqual(4000);
    expect(t.length).toBeGreaterThan(2000);
    expect(t[0]).toEqual(p[0]);
    expect(t[t.length - 1]).toEqual(p[p.length - 1]);
  });

  it("leaves a short route alone", () => {
    const p = route(100);
    expect(thinPath(p, 4000)).toBe(p);
  });

  it("returns a route with a bad coordinate unchanged, for the server to refuse", () => {
    const p = route(6000);
    p[10] = [NaN, 1];
    expect(thinPath(p, 4000)).toBe(p);
  });

  it("copes with a route that has no length", () => {
    expect(thinPath(Array.from({ length: 9000 }, () => [1, 2] as [number, number]), 4000)).toHaveLength(2);
  });
});
