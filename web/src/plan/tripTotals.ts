import { formatDuration } from "./format";
import { wholeDistance } from "./units";
import type { DistanceUnit } from "../lib/preferences";

// tripTotalsText is the plan_v2 one-line totals, the same words on the map's card and in the sheet's peek: "Day 2 · 228 mi · 1 charging stop · 4h 5m".
// The time is the plan's total, written as the summary strip writes it (plan/format), so the three places never show a total in different forms.
export function tripTotalsText(p: { day?: string; meters: number; stops: number; seconds: number; unit: DistanceUnit }): string {
  const parts: string[] = [];
  if (p.day) parts.push(p.day);
  if (p.meters > 0) parts.push(wholeDistance(p.meters, p.unit));
  if (p.stops > 0) parts.push(`${p.stops} charging stop${p.stops === 1 ? "" : "s"}`);
  if (p.seconds > 0) parts.push(formatDuration(p.seconds));
  return parts.join(" · ");
}

// timeSplit: the total's two parts, shown once beneath it. Driving is what is left of the total after charging, so the two always add up to it.
export function timeSplit(totalSec: number, chargingSec: number): { driving: string; charging: string } | null {
  if (!(totalSec > 0) || !(chargingSec > 0) || chargingSec >= totalSec) return null;
  return { driving: formatDuration(totalSec - chargingSec), charging: formatDuration(chargingSec) };
}
