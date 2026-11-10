import { describe, expect, it } from "vitest";
import { radarAsOf, radarTiles } from "./radar";

describe("radar", () => {
  it("labels the frame in local time", () => {
    const t = Date.UTC(2026, 10, 8, 18, 42) / 1000;
    expect(radarAsOf(t)).toMatch(/^Radar \d{1,2}:42/);
  });
  it("keys the tile url by frame time", () => {
    expect(radarTiles("https://x", 123)).toBe("https://x?t=123");
  });
});
