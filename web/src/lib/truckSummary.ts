import type { DrainSnapshot, PackHealthResponse } from "./api";
import { formatPct, view } from "./drain";

// The one line at the top of Truck (the scroll audit's "health headline"): the pack's
// usable capacity and the 7-day parked drain, each exactly as its card below shows it
// (PackHealthCard, DrainCard: the same figures, rounding and words), dropped when its card
// has no figure. Plain figures: no score, colour, comparison with the spec or "degradation".
// The iPhone's TruckLogic.summaryParts says the same.

export type SummaryPart = { id: "pack" | "drain"; text: string };

export function summaryParts(pack: PackHealthResponse | null | undefined, drain: DrainSnapshot | null | undefined): SummaryPart[] {
  const parts: SummaryPart[] = [];
  if (pack) {
    const reported = pack.headline.reported_kwh ?? 0;
    const samples = pack.samples ?? [];
    if (reported > 0) {
      parts.push({ id: "pack", text: `Pack ${reported.toFixed(1)} kWh usable, as your truck reports it` });
    } else if (samples.length > 0 && pack.headline.effective_kwh > 0) {
      parts.push({ id: "pack", text: `Pack about ${pack.headline.effective_kwh.toFixed(0)} kWh usable (estimate)` });
    }
  }
  if (drain) {
    const v = view(drain.windows["7d"], "7d");
    if (v.kind === "ok") parts.push({ id: "drain", text: `Parked drain ${formatPct(v.pctPerDay)}% a day (last 7 days)` });
  }
  return parts;
}

// summaryText is the line as one sentence (the halves after the first in lower case).
export function summaryText(parts: SummaryPart[]): string | null {
  if (parts.length === 0) return null;
  return parts.map((p, i) => (i === 0 ? p.text : p.text.charAt(0).toLowerCase() + p.text.slice(1))).join(" · ");
}
