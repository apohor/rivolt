import { describe, expect, it } from "vitest";
import { formatMinutes, formatMinutesShort } from "./minutes";

describe("formatMinutes", () => {
  it("minutes under an hour, hours plus minutes above", () => {
    expect(formatMinutes(0)).toBe("0 min");
    expect(formatMinutes(45)).toBe("45 min");
    expect(formatMinutes(59.6)).toBe("1 h");
    expect(formatMinutes(60)).toBe("1 h");
    expect(formatMinutes(130)).toBe("2 h 10 min");
    expect(formatMinutes(1337)).toBe("22 h 17 min");
  });
  it("never hides or caps big or odd values", () => {
    expect(formatMinutes(24 * 60)).toBe("24 h");
    expect(formatMinutes(-5)).toBe("0 min");
    expect(formatMinutes(NaN)).toBe("—");
  });
  it("has a compact form for axis ticks", () => {
    expect(formatMinutesShort(45)).toBe("45m");
    expect(formatMinutesShort(130)).toBe("2h 10m");
    expect(formatMinutesShort(180)).toBe("3h");
  });
});
