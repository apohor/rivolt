import type { DrainSnapshot, PackHealthResponse, TruckTrends } from "./api";
import { capacityView } from "./batteryCapacity";
import { formatPct, view } from "./drain";

// The one line at the top of Truck (the scroll audit's "health headline"): the battery's
// usable capacity and the 7-day parked drain, each exactly as its card below shows it
// (BatteryCapacityCard's headline, DrainCard: the same figures, rounding and words), dropped
// when its card has no figure. Plain figures: no score, colour, comparison with the spec or "degradation".
// The iPhone's TruckLogic.summaryParts says the same.

export type SummaryPart = { id: "pack" | "drain"; text: string };

export function summaryParts(
  pack: PackHealthResponse | null | undefined,
  trends: TruckTrends | null | undefined,
  drain: DrainSnapshot | null | undefined,
): SummaryPart[] {
  const parts: SummaryPart[] = [];
  // The Battery capacity card's headline: the truck's own figure, else our estimate (the trends median).
  const head = capacityView(pack, trends).head;
  if (head?.kind === "reported") parts.push({ id: "pack", text: `Pack ${head.kwh.toFixed(1)} kWh usable, as your truck reports it` });
  else if (head?.kind === "estimate") parts.push({ id: "pack", text: `Pack about ${head.kwh.toFixed(0)} kWh usable (estimate)` });
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
