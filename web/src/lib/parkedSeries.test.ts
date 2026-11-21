import { describe, expect, it } from "vitest";
import type { ParkedSeries } from "./api";
import { bandDuration, longDuration, openSleepSince, parkedDrawable, parkedChange, parkedText } from "./parkedSeries";

const base: ParkedSeries = {
  version: 1,
  from: "2026-11-19T12:00:00Z",
  to: "2026-11-20T12:00:00Z",
  parked_since: "2026-11-19T20:00:00Z",
  parked: true,
  change_pct: -2.4,
  points: [0, 1, 2, 3].map((i) => ({ t: `2026-11-19T${13 + i}:00:00Z`, soc: 80 - i, seg: 0 })),
  sleeps: [],
  wakes: [],
  figures: { asleep_today_s: 18 * 3600 + 40 * 60, wake_ups: 5, wake_cost_pct: 0.4, idle_pct_per_day: 6, idle_mi_per_day: 18 },
  projection: { at: "2026-11-23T14:00:00Z", soc: 61.2 },
};

describe("parked series text", () => {
  it("formats durations", () => {
    expect(longDuration(18 * 3600 + 40 * 60)).toBe("18 h 40 min");
    expect(longDuration(25 * 60)).toBe("25 min");
    expect(bandDuration(6 * 3600 + 10 * 60)).toBe("6 h 10");
    expect(bandDuration(5 * 3600 + 5 * 60)).toBe("5 h 05");
  });

  it("says the three figures in miles and kilometres", () => {
    const mi = parkedText(base, "mi").figures;
    expect(mi.map((f) => [f.value, f.note])).toEqual([
      ["18 h 40 min", ""],
      ["5 wake-ups", "0.4% each"],
      ["6% a day", "18 mi of range"],
    ]);
    expect(mi[2].tileNote).toBe("18 mi");
    expect(mi[1].rowValue).toBe("5");
    expect(parkedText(base, "km").figures[2].note).toBe("29 km of range");
  });

  it("leaves out what it cannot say", () => {
    const t = parkedText({ ...base, figures: { ...base.figures!, wake_ups: 1, wake_cost_pct: null, idle_pct_per_day: null, idle_mi_per_day: null }, projection: null }, "mi");
    expect(t.figures.map((f) => f.value)).toEqual(["18 h 40 min", "1 wake-up"]);
    expect(t.figures[1].note).toBe("");
    expect(t.projection).toBeNull();
    const none = parkedText({ ...base, figures: { ...base.figures!, wake_ups: 0, wake_cost_pct: null }, change_pct: null, parked_since: null }, "mi");
    expect(none.figures.map((f) => f.id)).toEqual(["asleep", "idle"]);
    // A cost that rounds to 0.0% is not printed; the count stands alone.
    const zero = parkedText({ ...base, figures: { ...base.figures!, wake_cost_pct: 0.02 } }, "mi");
    expect(zero.figures[1].note).toBe("");
    expect(zero.figures[1].tileNote).toBe("");
  });

  it("takes the start of the sleep the truck is in from the series", () => {
    const sl = (from: string, open: boolean) => ({ from, to: "2026-11-20T12:00:00Z", open });
    expect(openSleepSince({ ...base, sleeps: [sl("2026-11-20T01:34:30Z", true)] })).toBe(Date.parse("2026-11-20T01:34:30Z"));
    // began before the window: unknown; no open sleep: nothing to say
    expect(openSleepSince({ ...base, sleeps: [sl(base.from, true)] })).toBeNull();
    expect(openSleepSince({ ...base, sleeps: [sl("2026-11-20T01:34:30Z", false)] })).toBeUndefined();
    expect(openSleepSince({ ...base, sleeps: [] })).toBeUndefined();
  });

  it("writes the change as the difference of the two labels, with a true minus and one decimal", () => {
    expect(parkedChange(79.9, 77.9, null)).toEqual({ value: "−2.0%", since: "in 24 h" });
    expect(parkedChange(60, 60.5, null).value).toBe("+0.5%");
    expect(parkedChange(60, 60, null).value).toBe("No loss");
    expect(parkedChange(75.72, 75.7, null).value).toBe("No loss");
    expect(parkedChange(80, 77.6, Date.UTC(2026, 10, 19, 17, 20)).since).toMatch(/^since \d{1,2}:20/);
  });

  it("words the projection with the rounded battery and the estimate label", () => {
    expect(parkedText(base, "mi").projection).toMatch(/^At this rate: about 61% by \w+ .*\(estimate\)\.$/);
  });

  it("draws only with a line to draw", () => {
    expect(parkedDrawable(base)).toBe(true);
    expect(parkedDrawable({ ...base, points: base.points.slice(0, 3) })).toBe(false);
    expect(parkedDrawable(null)).toBe(false);
  });
});
