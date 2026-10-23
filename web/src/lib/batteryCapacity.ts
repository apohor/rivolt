import type { PackHealthResponse, TruckTrends } from "./api";
import { specKwh } from "../components/PackHealthCard";

// One figure for the battery's usable capacity (the owner: two numbers in two places confused).
// The headline is what the truck reports; without one, our estimate from charges (the trends
// median, the same history the dots show). The other figure goes on one labelled line. There is
// one estimate only: the pack-health "median of clean sessions" no longer shows.

export type CapacityView = {
  head: { kind: "reported"; kwh: number } | { kind: "estimate"; kwh: number } | null;
  // Our estimate from charges, when the server's rule for it is met.
  estimate: { kwh: number; charges: number } | null;
  // Rivian's spec, only when it is a usable figure (0 otherwise).
  spec: number;
  // How far along our estimate is, while it isn't ready.
  progress: string | null;
  // The truck can't be tracked (no capacity block at all, e.g. the R2).
  untracked: boolean;
  needPct: number;
};

export function capacityView(pack: PackHealthResponse | null | undefined, trends: TruckTrends | null | undefined): CapacityView {
  const reported = pack?.headline.reported_kwh ?? 0;
  const c = trends?.capacity ?? null;
  const needPct = c?.min_soc_delta_pct ?? 30;
  const estimate = c && c.median_kwh != null && c.samples.length >= 2 ? { kwh: c.median_kwh, charges: c.samples.length } : null;
  const spec = specKwh(pack?.headline.documented_kwh ?? c?.spec_kwh ?? 0);
  let progress: string | null = null;
  if (c && !estimate) {
    progress =
      `Our estimate needs about ${c.min_samples} charges that add at least ${needPct}% of battery, over at least ${c.min_span_days} days. ` +
      `So far: ${c.qualifying_count} of ${c.min_samples} charges` +
      (c.span_days < c.min_span_days ? `, over ${Math.round(c.span_days)} of ${c.min_span_days} days.` : ".");
  }
  const head = reported > 0 ? ({ kind: "reported", kwh: reported } as const) : estimate ? ({ kind: "estimate", kwh: estimate.kwh } as const) : null;
  return { head, estimate, spec, progress, untracked: !!trends && !c, needPct };
}

// The other figure, under the truck's own: our estimate, labelled as ours.
export function estimateLine(v: CapacityView): string | null {
  if (v.head?.kind !== "reported" || !v.estimate) return null;
  return `Our estimate from your charges: about ${v.estimate.kwh.toFixed(0)} kWh, from ${v.estimate.charges} ${v.estimate.charges === 1 ? "charge" : "charges"}.`;
}
