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

// OTHER_MIN_SEC: what the total holds beyond driving and charging (a rest, a wait) is named once it is 5 minutes or more; less is rounding.
const OTHER_MIN_SEC = 300;

// timeSplit: the total's parts, shown once beneath it. Driving is the plan's own driving time and charging its charging time; if the total holds
// more than the two (a rest, a wait), the remainder is shown as "other" rather than counted as driving. Null when a part is unknown (the plan
// has no driving time) or the parts overrun the total: the strip then keeps today's Charging cell.
export function timeSplit(totalSec: number, chargingSec: number, driveSec: number): { driving: string; charging: string; other: string | null } | null {
  if (!(totalSec > 0) || !(chargingSec > 0) || !(driveSec > 0)) return null;
  const rest = totalSec - driveSec - chargingSec;
  if (rest < -OTHER_MIN_SEC) return null;
  return { driving: formatDuration(driveSec), charging: formatDuration(chargingSec), other: rest >= OTHER_MIN_SEC ? formatDuration(rest) : null };
}
