import { describe, expect, it } from "vitest";
import { detectMoments } from "./driveTimelineData";

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
