import { describe, expect, it } from "vitest";
import type { DayActivity } from "./api";
import { lowestTire, sleepSummary, softwareInfo } from "./truckHealth";

describe("lowestTire", () => {
  it("ignores unreported wheels and flags a low reading", () => {
    const r = lowestTire({ tire_pressure_fl_bar: 0, tire_pressure_fr_bar: 2.1, tire_pressure_rl_bar: 0, tire_pressure_rr_bar: 2.9 });
    expect(r?.bar).toBe(2.1);
    expect(r?.low).toBe(true);
    expect(r?.psi).toBeCloseTo(30.5, 0);
  });
  it("is null when no wheel reported", () => {
    expect(lowestTire({ tire_pressure_fl_bar: 0, tire_pressure_fr_bar: 0, tire_pressure_rl_bar: 0, tire_pressure_rr_bar: 0 })).toBeNull();
  });
});

describe("softwareInfo", () => {
  it("hides when absent and only offers an update that differs", () => {
    expect(softwareInfo({ ota_current_version: "", ota_available_version: "" })).toBeNull();
    expect(softwareInfo({ ota_current_version: "2026.12", ota_available_version: "2026.12" })?.available).toBeNull();
    expect(softwareInfo({ ota_current_version: "2026.12", ota_available_version: "2026.14" })?.available).toBe("2026.14");
  });
});

describe("sleepSummary", () => {
  const day = (d: string, idle: number, sleeps: number, failed: number): DayActivity => ({
    day: `${d}T00:00:00Z`, asleep_h: 10, idle_awake_h: idle, sleeps, failed_sleeps: failed,
  });
  it("converts idle hours to minutes and flags trouble over two days", () => {
    const s = sleepSummary([day("2026-10-01", 1, 4, 0), day("2026-10-02", 2, 8, 2), day("2026-10-03", 0.5, 6, 2)]);
    expect(s.bars[1].value).toBe(120);
    expect(s.trouble).toEqual({ failed: 4, sleeps: 14 });
  });
  it("is empty and quiet without data", () => {
    const s = sleepSummary([]);
    expect(s.daysWithData).toBe(0);
    expect(s.trouble).toBeNull();
  });

  it("averages completed days only, leaving out the partial today", () => {
    const now = new Date("2026-10-03T08:00:00Z");
    const s = sleepSummary([day("2026-10-01", 2, 0, 0), day("2026-10-02", 4, 0, 0), day("2026-10-03", 0.1, 0, 0)], 3, now);
    expect(s.avgDays).toBe(2);
    expect(s.avgIdleMinutes).toBe(180);
  });
});
