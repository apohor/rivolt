import { describe, it, expect } from "vitest";
import {
  formatDuration,
  durationSeconds,
  num,
  pct,
  formatChargeState,
  isActiveCharge,
} from "./format";

describe("formatDuration", () => {
  it("renders hours and minutes", () => {
    expect(formatDuration(3600 + 23 * 60)).toBe("1h 23m");
  });
  it("renders minutes only under an hour", () => {
    expect(formatDuration(5 * 60)).toBe("5m");
  });
  it("falls back to a dash for non-positive or non-finite input", () => {
    expect(formatDuration(0)).toBe("—");
    expect(formatDuration(-10)).toBe("—");
    expect(formatDuration(NaN)).toBe("—");
  });
});

describe("durationSeconds", () => {
  it("returns the gap between two ISO timestamps in seconds", () => {
    expect(
      durationSeconds("2026-01-01T00:00:00Z", "2026-01-01T00:01:30Z"),
    ).toBe(90);
  });
});

describe("num", () => {
  it("formats with fixed precision and unit", () => {
    expect(num(3.14159, 2, "kWh")).toBe("3.14 kWh");
  });
  it("dashes out zero and non-finite values", () => {
    expect(num(0)).toBe("—");
    expect(num(Infinity)).toBe("—");
  });
});

describe("pct", () => {
  it("appends a percent sign", () => {
    expect(pct(87)).toBe("87%");
  });
  it("dashes out zero", () => {
    expect(pct(0)).toBe("—");
  });
});

describe("formatChargeState", () => {
  it("collapses a non-terminal Charging final state to a dash", () => {
    expect(formatChargeState("Charging")).toBe("—");
  });
  it("maps known terminal states", () => {
    expect(formatChargeState("Complete")).toBe("Complete");
    expect(formatChargeState("NoPower")).toBe("No power");
    expect(formatChargeState("charging_station_err")).toBe("Interrupted");
  });
  it("sentence-cases unknown snake_case values", () => {
    expect(formatChargeState("some_other_state")).toBe("Some other state");
  });
});

describe("isActiveCharge", () => {
  it("is true for a non-terminal charging_* state", () => {
    expect(isActiveCharge({ FinalState: "charging_active" })).toBe(true);
  });
  it("is false for terminal charging states and non-charging states", () => {
    expect(isActiveCharge({ FinalState: "charging_complete" })).toBe(false);
    expect(isActiveCharge({ FinalState: "charging_station_err" })).toBe(false);
    expect(isActiveCharge({ FinalState: "Complete" })).toBe(false);
    expect(isActiveCharge({ FinalState: "" })).toBe(false);
  });
});
