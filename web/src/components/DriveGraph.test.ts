import { describe, expect, it } from "vitest";
import type { Sample } from "../lib/api";
import { driveSeries } from "./DriveGraph";

const T0 = Date.parse("2026-11-20T12:00:00Z");
const row = (sec: number, mph: number, pct = 70): Sample => ({ At: new Date(T0 + sec * 1000).toISOString(), SpeedMph: mph, BatteryLevelPct: pct }) as Sample;
const run = (secs: number, step = 10) => Array.from({ length: Math.floor(secs / step) + 1 }, (_, i) => row(i * step, 40, 70 - i * 0.05));

describe("driveSeries", () => {
  it("gives no series for a drive that just started", () => {
    expect(driveSeries(run(30), T0)).toBeNull();
    expect(driveSeries([], T0)).toBeNull();
  });
  it("needs both the points and the span", () => {
    expect(driveSeries([row(0, 10), row(120, 30)], T0)).toBeNull();
  });
  it("draws from the start of the drive, not from earlier samples", () => {
    const rows = [row(-600, 0, 80), row(-300, 0, 80), ...run(300)];
    const s = driveSeries(rows, T0)!;
    expect(s.speed[0].x).toBeGreaterThanOrEqual(T0 - 5_000);
    expect(s.startPct).toBeCloseTo(70, 5);
  });
  it("a stop reads as zero speed, never negative", () => {
    const s = driveSeries([...run(120), row(130, -2), row(140, 0), row(150, 0)], T0)!;
    expect(Math.min(...s.speed.map((p) => p.y))).toBeGreaterThanOrEqual(0);
  });
});
