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
  const now = Date.parse("2026-10-15T12:00:00Z");
  const iso = (minAgo: number) => new Date(now - minAgo * 60_000).toISOString();
  it("is true for an open state with a recent last sample", () => {
    expect(isActiveCharge({ FinalState: "charging_active", EndedAt: iso(1) }, now)).toBe(true);
    expect(isActiveCharge({ FinalState: "charging_connecting", EndedAt: iso(20) }, now)).toBe(true);
    expect(isActiveCharge({ FinalState: "charging_active" }, now)).toBe(true);
  });
  it("is false for every ended state, not just two of them (prod has 97 charging_user_stopped rows)", () => {
    for (const s of [
      "charging_complete",
      "charging_station_err",
      "charging_station_error",
      "charging_station_stopped",
      "charging_user_stopped",
      "charging_vehicle_error",
      "charging_scheduled",
      "Complete",
      "",
    ]) {
      expect(isActiveCharge({ FinalState: s, EndedAt: iso(1) }, now)).toBe(false);
    }
  });
  it("is false when the session is stuck open but the last sample is old", () => {
    expect(isActiveCharge({ FinalState: "charging_active", EndedAt: iso(60) }, now)).toBe(false);
    expect(isActiveCharge({ FinalState: "charging_ready", EndedAt: iso(3 * 24 * 60) }, now)).toBe(false);
  });
});
