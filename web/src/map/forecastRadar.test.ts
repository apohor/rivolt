import { describe, expect, it } from "vitest";
import { forecastLabel, forecastSlots, forecastTileURL, inBounds, LINE_OUTSIDE_US, LINE_PAST_HORIZON, slotLine } from "./forecastRadar";

const H = 3600;
const base = 1_800_000_000 - (1_800_000_000 % H); // an hour boundary
const hours = (n: number, from = 0) => Array.from({ length: n }, (_, i) => ({ fh: from + i, valid_unix: base + (from + i) * H }));

describe("forecastSlots", () => {
  it("runs from the departure hour to the arrival hour, one an hour", () => {
    const s = forecastSlots(hours(10), base + 20 * 60, base + 3 * H + 5 * 60);
    expect(s.map((x) => x.t)).toEqual([base, base + H, base + 2 * H, base + 3 * H]);
    expect(s.map((x) => x.fh)).toEqual([0, 1, 2, 3]);
  });
  it("marks the hours the forecast does not have (past its horizon, before its first hour)", () => {
    const s = forecastSlots(hours(3, 1), base, base + 4 * H);
    expect(s.map((x) => x.fh)).toEqual([null, 1, 2, 3, null]);
  });
  it("is one slot for a trip inside one hour, and none without a departure time", () => {
    expect(forecastSlots(hours(5), base + 10, base + 50)).toHaveLength(1);
    expect(forecastSlots(hours(5), 0, 0)).toEqual([]);
  });
  it("caps at 48 hours", () => {
    expect(forecastSlots(hours(60), base, base + 100 * H)).toHaveLength(48);
  });
});

describe("lines and labels", () => {
  it("says the one plain line, or nothing when the hour is drawn", () => {
    expect(slotLine({ t: base, fh: 2 }, true)).toBe("");
    expect(slotLine({ t: base, fh: null }, true)).toBe(LINE_PAST_HORIZON);
    expect(slotLine(undefined, true)).toBe(LINE_PAST_HORIZON);
    expect(slotLine({ t: base, fh: 2 }, false)).toBe(LINE_OUTSIDE_US);
  });
  it("puts the cycle and hour in the tile address", () => {
    expect(forecastTileURL("https://x", "2026112712", 5)).toBe("https://x/api/maps/forecast-radar/2026112712/5/{z}/{x}/{y}.png");
  });
  it("tests the grid's box", () => {
    const b: [number, number, number, number] = [-125, 21, -66, 53];
    expect(inBounds(b, 40, -100)).toBe(true);
    expect(inBounds(b, 51, -114)).toBe(true);
    expect(inBounds(b, 55, -100)).toBe(false);
    expect(inBounds(b, 40, 10)).toBe(false);
  });
  it("labels the hour", () => {
    expect(forecastLabel(base)).toMatch(/^Forecast for \d{1,2}\s?(am|pm)$/);
  });
});
