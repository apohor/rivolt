import { describe, expect, it } from "vitest";
import { agoFrom } from "./adminFormat";

const NOW = Date.parse("2026-10-18T12:00:00Z");
describe("agoFrom", () => {
  it("reads seconds, minutes, hours and days", () => {
    const t = (ms: number) => new Date(NOW - ms).toISOString();
    expect(agoFrom(t(5_000), NOW)).toBe("5s ago");
    expect(agoFrom(t(5 * 60_000), NOW)).toBe("5m ago");
    expect(agoFrom(t(3 * 3_600_000), NOW)).toBe("3h ago");
    expect(agoFrom(t(2 * 86_400_000), NOW)).toBe("2d ago");
  });
  it("says never for nothing, a dash for junk, and never goes negative", () => {
    expect(agoFrom(null, NOW)).toBe("never");
    expect(agoFrom("not a date", NOW)).toBe("—");
    expect(agoFrom(new Date(NOW + 60_000).toISOString(), NOW)).toBe("0s ago");
  });
});
