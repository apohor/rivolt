import { describe, expect, it } from "vitest";
import type { DrainSnapshot, DrainWindow, PackHealthResponse } from "./api";
import { formatPct } from "./drain";
import { summaryParts, summaryText } from "./truckSummary";

// Synthetic figures. The line says exactly what the Pack capacity and Parked drain (7 days)
// cards say: the same figures, rounding and words, each dropped when its card has none.
const headline = { effective_kwh: 121.6, nameplate_kwh: 135, pct_of_nameplate: 90, documented_kwh: 135, reported_kwh: 123, reported_pct_of_documented: 91, sample_count: 5, window: 5 };
const sample = { vehicle_id: "v", charge_id: "c", at: "2026-10-01T00:00:00Z", pack_kwh_effective: 121.6, soc_delta_pct: 50, energy_delivered_kwh: 60, derate_active: false };
const pack = (o: Partial<typeof headline>, samples = [sample]) => ({ samples, headline: { ...headline, ...o } }) as unknown as PackHealthResponse;
const win = (sufficient: boolean, pct: number | null) => ({ sufficient, drain_pct_per_day: pct, drain_kwh_per_day: 0.8, parked_hours: 100, days_with_data: 7, days_excluded: { insufficient: 0, charging: 0, driving: 0 }, sleeps: 20, quick_rewakes: 1 }) as unknown as DrainWindow;
const drain = (w: DrainWindow) => ({ windows: { "7d": w, "30d": win(true, 1.5) } }) as unknown as DrainSnapshot;

describe("truck summary line", () => {
  it("equals the cards it summarises", () => {
    const parts = summaryParts(pack({}), drain(win(true, 0.6)));
    expect(parts.map((p) => p.id)).toEqual(["pack", "drain"]);
    expect(summaryText(parts)).toBe(`Pack ${(123).toFixed(1)} kWh usable, as your truck reports it · parked drain ${formatPct(0.6)}% a day (last 7 days)`);
    expect(summaryText(parts)).toBe("Pack 123.0 kWh usable, as your truck reports it · parked drain 0.6% a day (last 7 days)");
  });

  it("calls our estimate one, and drops what a card can't show", () => {
    expect(summaryText(summaryParts(pack({ reported_kwh: 0 }), null))).toBe("Pack about 122 kWh usable (estimate)");
    // No samples and no figure from the truck: the card shows none, so neither does the line.
    expect(summaryParts(pack({ reported_kwh: 0 }, []), null)).toEqual([]);
    // The 7-day window must be sufficient; the 30-day one is never used.
    expect(summaryText(summaryParts(null, drain(win(true, 12.4))))).toBe("Parked drain 12% a day (last 7 days)");
    expect(summaryParts(null, drain(win(false, 0.6)))).toEqual([]);
    expect(summaryText(summaryParts(null, null))).toBeNull();
  });

  it("names no score, comparison or loss", () => {
    const line = summaryText(summaryParts(pack({}), drain(win(true, 0.6))))!.toLowerCase();
    for (const w of ["health", "degrad", "loss", "lost", "% of", "spec"]) expect(line).not.toContain(w);
  });
});
