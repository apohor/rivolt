import { describe, expect, it } from "vitest";
import { chargeRingLabel, chargeRingSvg, ringArc } from "./chargeRing";

describe("charge ring", () => {
  it("the arc runs from the arrival to the departure charge", () => {
    expect(ringArc(25, 63)).toEqual({ start: 0.25, length: 0.38 });
  });
  it("unknown or equal charges draw the track and the bolt only", () => {
    expect(ringArc(null, 80)).toBeNull();
    expect(ringArc(40, undefined)).toBeNull();
    expect(ringArc(NaN, 80)).toBeNull();
    expect(ringArc(50, 50)).toBeNull();
    expect(ringArc(80, 40)).toBeNull();
    expect(chargeRingSvg(30, 50, 50)).not.toContain("stroke-dasharray");
    expect(chargeRingSvg(30, 50, 50)).toContain("var(--cr-track)");
  });
  it("clamps to 0..100 and a full arc still draws", () => {
    expect(ringArc(-20, 130)).toEqual({ start: 0, length: 1 });
    const full = chargeRingSvg(30, 0, 100);
    const [len, circ] = /stroke-dasharray="([\d.]+) ([\d.]+)"/.exec(full)!.slice(1);
    expect(len).toBe(circ);
  });
  it("the label keeps the order and the range", () => {
    expect(chargeRingLabel(1, 2, 24.6, 63.2)).toBe("Charging stop 1 of 2, 25 to 63 percent");
    expect(chargeRingLabel(2, 2, null, 80)).toBe("Charging stop 2 of 2");
  });
});
