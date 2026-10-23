import { describe, expect, it } from "vitest";
import type { PackHealthResponse, TruckTrends } from "./api";
import { capacityView, estimateLine } from "./batteryCapacity";

// Synthetic figures. One headline (the truck's own, else our estimate), the other figure on one
// labelled line, one estimate source only (the trends median).
const pack = (reported: number) =>
  ({ samples: [], headline: { effective_kwh: 121.6, nameplate_kwh: 135, pct_of_nameplate: 90, documented_kwh: 135, reported_kwh: reported, reported_pct_of_documented: 0, sample_count: 5, window: 5 } }) as unknown as PackHealthResponse;
const samples = Array.from({ length: 20 }, (_, i) => ({ at: `2026-0${1 + (i % 9)}-10T00:00:00Z`, kwh_effective: 138 }));
const trends = (cap: Partial<NonNullable<TruckTrends["capacity"]>> | null) =>
  ({ capacity: cap === null ? null : { samples, median_kwh: 138.4, spec_kwh: 141, qualifying_count: 20, span_days: 290, min_samples: 5, min_span_days: 30, ...cap } }) as unknown as TruckTrends;

describe("battery capacity card", () => {
  it("leads with the truck's figure and labels ours", () => {
    const v = capacityView(pack(135), trends({}));
    expect(v.head).toEqual({ kind: "reported", kwh: 135 });
    expect(estimateLine(v)).toBe("Our estimate from your charges: about 138 kWh, from 20 charges.");
    expect(v.spec).toBe(135);
  });
  it("leads with our estimate when the truck sends none, with no second line", () => {
    const v = capacityView(pack(0), trends({}));
    expect(v.head).toEqual({ kind: "estimate", kwh: 138.4 });
    expect(estimateLine(v)).toBeNull();
  });
  it("says how far along our estimate is, or that it can't be tracked", () => {
    const sparse = capacityView(pack(135), trends({ median_kwh: null, samples: samples.slice(0, 1), qualifying_count: 2, span_days: 3 }));
    expect(sparse.progress).toBe("Our estimate needs about 5 charges that add at least 30% of battery, over at least 30 days. So far: 2 of 5 charges, over 3 of 30 days.");
    expect(estimateLine(sparse)).toBeNull();
    expect(capacityView(pack(135), trends(null)).untracked).toBe(true);
  });
  it("names no score, comparison or loss", () => {
    const v = capacityView(pack(135), trends({}));
    const words = `${estimateLine(v)} ${v.progress ?? ""}`.toLowerCase();
    for (const w of ["health", "degrad", "loss", "lost", "% of"]) expect(words).not.toContain(w);
  });
});
