import { describe, expect, it } from "vitest";
import { END_COLOR, endpointColor, endpointGlow, START_COLOR } from "./endpointColors";

describe("endpointColors", () => {
  it("is the map's green start and rose destination", () => {
    expect(START_COLOR).toBe("#10b981");
    expect(END_COLOR).toBe("#f43f5e");
    expect(endpointColor("start")).toBe(START_COLOR);
    expect(endpointColor("end")).toBe(END_COLOR);
  });
  it("gives a round trip's end the start's colour (one pin on the map)", () => {
    expect(endpointColor("end", true)).toBe(START_COLOR);
  });
  it("glows in the dot's own colour", () => {
    expect(endpointGlow("#10b981")).toContain("#10b981");
  });
});
