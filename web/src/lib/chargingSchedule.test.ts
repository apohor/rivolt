import { describe, expect, it } from "vitest";
import { formatScheduleStart, localParts, nextScheduledStartMin, type ChargingSchedule } from "./chargingSchedule";

// Wed 2026-10-14, 21:30 in New York (UTC-4).
const now = new Date("2026-10-15T01:30:00Z");
const TZ = "America/New_York";
const s = (o: Partial<ChargingSchedule> = {}): ChargingSchedule => ({
  start_min: 23 * 60, duration_min: 480, enabled: true, weekdays: ["Wednesday"], applies_here: true, ...o,
});

describe("localParts", () => {
  it("reads weekday and minutes in the given zone", () => {
    expect(localParts(now, TZ)).toEqual({ weekday: "Wednesday", minutes: 21 * 60 + 30 });
    expect(localParts(now, "UTC").weekday).toBe("Thursday");
  });
});

describe("nextScheduledStartMin", () => {
  it("returns today's upcoming start", () => {
    expect(nextScheduledStartMin([s()], now, TZ)).toBe(23 * 60);
  });
  it("takes the earliest of several", () => {
    expect(nextScheduledStartMin([s({ start_min: 23 * 60 + 30 }), s({ start_min: 22 * 60 })], now, TZ)).toBe(22 * 60);
  });
  it("ignores disabled, elsewhere, unknown-place and other-weekday schedules", () => {
    expect(nextScheduledStartMin([s({ enabled: false })], now, TZ)).toBeNull();
    expect(nextScheduledStartMin([s({ applies_here: false })], now, TZ)).toBeNull();
    expect(nextScheduledStartMin([s({ applies_here: null })], now, TZ)).toBeNull();
    expect(nextScheduledStartMin([s({ applies_here: undefined })], now, TZ)).toBeNull();
    expect(nextScheduledStartMin([s({ weekdays: ["Thursday"] })], now, TZ)).toBeNull();
  });
  it("is null once the window has started (the truck would be charging)", () => {
    expect(nextScheduledStartMin([s({ start_min: 21 * 60 })], now, TZ)).toBeNull();
    expect(nextScheduledStartMin([s({ start_min: 21 * 60 + 30 })], now, TZ)).toBeNull();
  });
  it("is null with no schedules", () => {
    expect(nextScheduledStartMin([], now, TZ)).toBeNull();
    expect(nextScheduledStartMin(undefined, now, TZ)).toBeNull();
  });
});

describe("formatScheduleStart", () => {
  it("formats minutes after midnight as a clock time", () => {
    expect(formatScheduleStart(23 * 60)).toMatch(/11:00\s?PM|23:00/);
    expect(formatScheduleStart(0 * 60 + 5)).toMatch(/12:05\s?AM|00:05/);
  });
});
