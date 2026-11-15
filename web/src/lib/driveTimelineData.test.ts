import { describe, expect, it } from "vitest";
import { detectMoments, labelGap, thinTicks } from "./driveTimelineData";

const T0 = 1_700_000_000_000;
const pts = (ys: number[], stepMs = 60_000) => ys.map((y, i) => ({ x: T0 + i * stepMs, y }));

describe("detectMoments · Lowest SoC", () => {
  it("is not emitted when the minimum is the end of the drive", () => {
    const soc = pts([80, 78, 76, 74, 72, 70, 68, 66]);
    const m = detectMoments(pts(soc.map(() => 30)), soc, [], []);
    expect(m.some((x) => x.label === "Lowest SoC")).toBe(false);
  });
  it("is emitted when SoC dipped mid-drive and recovered (regen)", () => {
    const soc = pts([80, 70, 60, 50, 55, 60, 64, 66]);
    const m = detectMoments(pts(soc.map(() => 30)), soc, [], []);
    expect(m.some((x) => x.label === "Lowest SoC")).toBe(true);
  });
});

describe("thinTicks", () => {
  it("keeps well-spaced ticks as they are", () => {
    expect(thinTicks([50, 150, 250, 350], 48)).toEqual([0, 1, 2, 3]);
  });
  it("drops the ticks squeezed together by a parked stretch", () => {
    // 10:00, 10:03 and 10:08 land within 26 px of each other; only the first stays, then the next clear one.
    expect(thinTicks([40, 52, 66, 200, 340], 48)).toEqual([0, 3, 4]);
  });
  it("takes the room from each pair's real label widths", () => {
    const w = [30, 60, 30, 30];
    const gap = (a: number, b: number) => (w[a] + w[b]) / 2 + 10;
    // 1 is 60 px wide: 0 to 1 needs 55, so a tick 50 px on is dropped; 2 is far enough from 0 (40).
    expect(thinTicks([0, 50, 100, 200], gap)).toEqual([0, 2, 3]);
    expect(thinTicks([0, 60, 120, 200], gap)).toEqual([0, 1, 2, 3]);
  });
  it("gives an edge-anchored label its full width on the inward side", () => {
    const w = [50, 50, 50];
    const centred = labelGap(w, false, false, 10);
    const edged = labelGap(w, true, true, 10);
    expect(centred(0, 1)).toBe(60);
    expect(edged(0, 1)).toBe(85); // first label starts at its tick
    expect(edged(1, 2)).toBe(85); // last label ends at its tick
    expect(edged(0, 2)).toBe(110);
    // 10:45 PM-like labels, first tick 10 px from the edge: the second must clear the whole first label
    expect(thinTicks([0, 70, 200], edged)).toEqual([0, 2]);
    expect(thinTicks([0, 70, 200], centred)).toEqual([0, 1, 2]);
  });
  it("keeps the last tick and drops the one before it when they collide", () => {
    expect(thinTicks([40, 200, 330, 350], 48)).toEqual([0, 1, 3]);
  });
});
