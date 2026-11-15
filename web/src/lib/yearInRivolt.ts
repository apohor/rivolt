// "Your year in Rivolt": the January card. The figures come from the server (GET /api/year-in-rivolt, the signed-in user's own year, UTC);
// this builds the share card from them. Like every share card the model carries display text only: no place, day, time, VIN or truck
// name can reach the painter. Cost is opt-in every time the sheet opens.

import type { YearInRivolt } from "./api";
import { formatMoney } from "./ledger";
import type { DistanceUnit, TemperatureUnit } from "./preferences";
import type { ShareCardModel, ShareStat } from "./shareCard";

// The card is offered in January for the year just ended.
export function yearCardYear(now: Date = new Date()): number | null {
  return now.getMonth() === 0 ? now.getFullYear() - 1 : null;
}

const n0 = (x: number) => Math.round(x).toLocaleString();
const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

// "2026-07" -> "July"; a malformed month is dropped, never shown as "Invalid Date".
export function monthName(ym: string | undefined): string {
  const m = ym ? /^(\d{4})-(\d{2})$/.exec(ym) : null;
  if (!m || Number(m[2]) < 1 || Number(m[2]) > 12) return "";
  return new Date(`${ym}-15T12:00:00Z`).toLocaleDateString("en-US", { month: "long", timeZone: "UTC" });
}

export function yearFileName(year: number): string {
  return `rivolt-year-${year}.png`;
}

// The card has room for six rows. Cost, when asked for, takes the third.
export function buildYearShareModel(y: YearInRivolt, o: { includeCost: boolean; unit?: DistanceUnit; tempUnit?: TemperatureUnit }): ShareCardModel | null {
  if (!y.eligible || !y.miles || y.miles <= 0) return null;
  // The figures arrive in miles and Fahrenheit; the card says them in the user's units, like the pages do.
  const unit = o.unit ?? "mi";
  const k = unit === "km" ? 1.609344 : 1;
  const rows: ShareStat[] = [{ label: "Driven", value: `${n0(y.miles * k)} ${unit}` }];
  if (y.kwh && y.kwh > 0) rows.push({ label: "kWh used driving", value: `${n0(y.kwh)} kWh` });
  if (o.includeCost && y.cost) {
    const approx = y.cost.estimated ? "~" : "";
    const perMile = y.cost_per_mile && y.cost_per_mile > 0 ? ` · ${approx}${formatMoney(y.cost_per_mile / k, y.cost.currency, 2)}/${unit}` : "";
    rows.push({ label: y.cost.estimated ? "Cost (estimate)" : "Cost", value: `${approx}${formatMoney(y.cost.amount, y.cost.currency, 0)}${perMile}` });
  }
  // The year's efficiency is an estimate by construction, so it always says so.
  if (y.efficiency_mi_per_kwh && y.efficiency_mi_per_kwh > 0) rows.push({ label: "Efficiency", value: `about ${(y.efficiency_mi_per_kwh * k).toFixed(1)} ${unit}/kWh` });
  if (y.longest_trip && y.longest_trip.miles > 0) {
    const mon = monthName(y.longest_trip.month);
    rows.push({ label: "Longest trip", value: `${n0(y.longest_trip.miles * k)} ${unit}${mon ? ` · ${mon}` : ""}` });
  }
  const s = y.energy_shares;
  if (s) {
    const pct = (f: number) => `${Math.round(Math.min(1, Math.max(0, f)) * 100)}%`;
    rows.push({ label: "Charged", value: `${pct(s.home)} home · ${pct(s.away)} away · ${pct(s.fast)} fast` });
  }
  if (y.coldest_drive) {
    const mon = monthName(y.coldest_drive.month);
    rows.push({ label: "Coldest drive", value: `${o.tempUnit === "c" ? Math.round((y.coldest_drive.temp_f - 32) / 1.8) : Math.round(y.coldest_drive.temp_f)}°${o.tempUnit === "c" ? "C" : "F"}${mon ? ` · ${mon}` : ""}` });
  }
  if (y.most_efficient_long_drive) {
    rows.push({ label: "Best long drive", value: `${(y.most_efficient_long_drive.mi_per_kwh * k).toFixed(1)} ${unit}/kWh · ${n0(y.most_efficient_long_drive.miles * k)} ${unit}` });
  }
  const parts: string[] = [];
  if (y.drive_count && y.drive_count > 0) parts.push(`${n0(y.drive_count)} ${plural(y.drive_count, "drive", "drives")}`);
  if (y.charge_count && y.charge_count > 0) parts.push(`${n0(y.charge_count)} ${plural(y.charge_count, "charge", "charges")}`);
  // Zero is not a story: the line is hidden rather than "0 stops on 0 trips".
  if (y.planned_trips && y.planned_trips > 0 && y.planned_stops && y.planned_stops > 0) {
    parts.push(`${n0(y.planned_stops)} ${plural(y.planned_stops, "stop", "stops")} on ${n0(y.planned_trips)} planned ${plural(y.planned_trips, "trip", "trips")}`);
  }
  const since = monthName(y.since_month);
  return {
    kind: "year",
    eyebrow: since ? `Recorded by Rivolt · since ${since}` : "Recorded by Rivolt",
    title: y.model ? `${y.year} in your ${y.model}` : `${y.year} in Rivolt`,
    subtitle: parts.join(" · "),
    stats: rows.slice(0, 6),
    // No note line: the cost row says "estimate" itself, and a note would run into the footer on a full card.
    notes: [],
    sample: false,
  };
}
