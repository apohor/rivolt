import { describe, expect, it } from "vitest";
import { radarAsOf, radarSpoken, radarTiles } from "./radar";

describe("radar", () => {
  it("labels the frame in local time", () => {
    const t = Date.UTC(2026, 10, 8, 18, 42) / 1000;
    expect(radarAsOf(t)).toMatch(/^NOAA\/NWS · \d{1,2}:42/);
    expect(radarSpoken(t)).toMatch(/^light, moderate, heavy, NOAA\/NWS radar, \d{1,2}:42/);
  });
  it("keys the tile url by frame time", () => {
    expect(radarTiles("https://x", 123)).toBe("https://x?t=123");
  });
});
