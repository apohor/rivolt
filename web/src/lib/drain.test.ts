import { describe, expect, it } from "vitest";
import type { DrainDay, DrainWindow } from "./api";
import { bars, formatHours, formatPct, view } from "./drain";

const win = (o: Partial<DrainWindow> = {}): DrainWindow => ({
  drain_pct_per_day: 0.9, drain_kwh_per_day: 1.2, idle_awake_hours_per_day: 1.3, sleeps: 22, quick_rewakes: 3,
  median_sleep_seconds: 18000, parked_hours: 120, power_state_observed_hours: 150, days_in_window: 7,
  days_with_data: 6, days_excluded: { charging: 1, driving: 0, insufficient: 0 }, sufficient: true, ...o,
});

describe("view", () => {
  it("never yields a rate when data is insufficient, even if one is present", () => {
    const v = view(win({ sufficient: false, drain_pct_per_day: 4.2, parked_hours: 9 }), "7d");
    expect(v).toEqual({ kind: "insufficient", haveHours: 9, needHours: 24 });
  });
  it("treats a null rate as insufficient", () => {
    expect(view(win({ drain_pct_per_day: null }), "30d").kind).toBe("insufficient");
  });
  it("carries the figures and counts left-out days", () => {
    const v = view(win({ days_excluded: { charging: 1, driving: 1, insufficient: 2 } }), "7d");
    expect(v.kind === "ok" && v.daysExcluded).toBe(4);
    expect(v.kind === "ok" && v.kwhPerDay).toBe(1.2);
  });
});

describe("formatting", () => {
  it("keeps one decimal under 10 and rounds above", () => {
    expect(formatPct(0.94)).toBe("0.9");
    expect(formatPct(12.4)).toBe("12");
    expect(formatHours(0.5)).toBe("30 min");
    expect(formatHours(1.34)).toBe("1.3 h");
  });
});

describe("bars", () => {
  const d = (day: string, drain: number | null, excluded: DrainDay["excluded"] = null): DrainDay => ({
    day, drain_pct: drain, idle_awake_hours: 1, parked_hours: 20, sleeps: 2, quick_rewakes: 0, excluded,
  });
  it("leaves excluded and unmeasured days valueless, scaling the rest", () => {
    const r = bars([d("a", 1), d("b", 2), d("c", 5, "charging"), d("d", null)]);
    expect(r.bars.map((b) => b.value)).toEqual([1, 2, null, null]);
    expect(r.max).toBe(2);
    expect(r.bars[1].h).toBe(1);
  });
  it("has a floor on the scale so a near-zero month doesn't look huge", () => {
    expect(bars([d("a", 0.05)]).max).toBe(0.5);
  });
});
