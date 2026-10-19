import { describe, expect, it } from "vitest";
import type { SavedTripInputs } from "../lib/api";
import { multiDayFirst, tripSubtitle, tripTag } from "./tripSubtitle";

const pt = (label: string, lat: number, lon: number) => ({ label, lat, lon });
const home = pt("Rose Bowl parking (sample)", 34.16, -118.17);
const hotel = pt("Las Vegas hotel (sample)", 36.17, -115.14);
const inp = (extra: Partial<SavedTripInputs>): SavedTripInputs => ({ origin: home, destination: home, extra_stops: [], ...extra }) as SavedTripInputs;

describe("tripSubtitle", () => {
  it("keeps a single day as origin → end", () => {
    expect(tripSubtitle(inp({ destination: pt("Las Vegas, NV", 36, -115) }))).toBe("Rose Bowl parking (sample) → Las Vegas, NV");
  });
  it("reads a round trip as there-and-back, not the same place twice", () => {
    expect(tripSubtitle(inp({ extra_stops: [hotel], overnight_flags: [true] }))).toBe("Rose Bowl parking ↔ Las Vegas hotel · 2 days");
  });
  it("says how many overnights when there are several, and where for one-way trips", () => {
    expect(tripSubtitle(inp({ destination: pt("Denver", 39, -104), extra_stops: [pt("Des Moines", 41, -93)], overnight_flags: [true] }))).toBe(
      "Rose Bowl parking → Denver · 2 days · overnight in Des Moines",
    );
    expect(tripSubtitle(inp({ destination: pt("Denver", 39, -104), extra_stops: [pt("A", 1, 1), pt("B", 2, 2)], overnight_flags: [true, true] }))).toBe(
      "Rose Bowl parking → Denver · 3 days · 2 overnights",
    );
  });
  it("falls back to a round trip line when the far stop is the start", () => {
    expect(tripSubtitle(inp({ extra_stops: [pt("Rose Bowl parking", 34.1601, -118.1701)], overnight_flags: [true] }))).toBe("Rose Bowl parking round trip · 2 days");
  });
});

describe("tripTag and ordering", () => {
  it("tags a hotel night and puts multi-day samples first", () => {
    const multi = inp({ extra_stops: [hotel], overnight_flags: [true] });
    const single = inp({ destination: pt("X", 1, 1) });
    expect(tripTag(multi)).toBe("2 days · hotel night");
    expect(tripTag(single)).toBeNull();
    expect(multiDayFirst([{ inputs: single, id: "a" }, { inputs: multi, id: "b" }, { inputs: single, id: "c" }]).map((t) => t.id)).toEqual(["b", "a", "c"]);
  });
});
