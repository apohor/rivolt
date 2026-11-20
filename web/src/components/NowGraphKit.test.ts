import { describe, expect, it } from "vitest";
import { easeSteps, softPath } from "./NowGraphKit";

describe("easeSteps", () => {
  it("leaves a gentle line alone", () => {
    const pts = [{ x: 0, y: 10 }, { x: 40, y: 12 }, { x: 80, y: 13 }];
    expect(easeSteps(pts)).toEqual(pts);
  });

  it("spreads a jump into a level-ended ramp that ends on the second reading", () => {
    const out = easeSteps([{ x: 0, y: 10 }, { x: 100, y: 10 }, { x: 103, y: 40 }, { x: 200, y: 41 }]);
    expect(out).toHaveLength(4);
    expect(out[2]).toEqual({ x: 103, y: 40, hold: true });
    expect(out[1].hold).toBe(true);
    expect(out[1].y).toBe(10);
    expect(out[2].x - out[1].x).toBeGreaterThanOrEqual(12);
    expect(out[1].x).toBeGreaterThan(out[0].x);
  });

  it("eases two jumps in a row", () => {
    const out = easeSteps([{ x: 0, y: 0 }, { x: 100, y: 0 }, { x: 103, y: 30 }, { x: 160, y: 30 }, { x: 163, y: 60 }, { x: 220, y: 60 }]);
    expect(out.filter((p) => p.hold).length).toBeGreaterThanOrEqual(4);
    expect(out.find((p) => p.x === 163)?.hold).toBe(true);
    const into = out.findIndex((p) => p.x === 163);
    expect(out[into - 1].hold).toBe(true);
    expect(out[into].x - out[into - 1].x).toBeGreaterThanOrEqual(12);
  });

  it("never reorders the readings", () => {
    const out = easeSteps([{ x: 0, y: 0 }, { x: 5, y: 0 }, { x: 7, y: 30 }, { x: 9, y: 31 }, { x: 11, y: 70 }, { x: 40, y: 71 }]);
    for (let i = 1; i < out.length; i++) expect(out[i].x).toBeGreaterThanOrEqual(out[i - 1].x);
  });
});

describe("softPath", () => {
  it("is level at held points", () => {
    const d = softPath([{ x: 0, y: 0, hold: true }, { x: 20, y: 30, hold: true }]);
    // Both control points sit at their own end's height: no slope at either end.
    expect(d).toBe("M0.0 0.0 C6.7 0.0 13.3 30.0 20.0 30.0");
  });
});
