// Pure presentation model for Plan vs Actual: which rows to show, with
// deltas only where both sides are known (never a guessed number).

import type { PlanVsActual, PvaActual, PvaPlanned } from "./api";

export type PvaState = "none" | "suggested" | "matched-full" | "matched-partial";

export function stateOf(p: PlanVsActual): PvaState {
  if (p.status === "none") return "none";
  if (p.status === "suggested") return "suggested";
  const a = p.actual;
  if (!a) return "none";
  const unknownCost = a.cost == null || a.cost_source === "unknown" || a.unpriced_sessions > 0;
  return unknownCost || a.kwh == null ? "matched-partial" : "matched-full";
}

export type Row = {
  key: "cost" | "kwh" | "stops" | "arrival_soc" | "duration";
  label: string;
  planned: string | null;
  actual: string | null;
  // Signed difference in the metric's own unit; null when either side is unknown.
  delta: number | null;
  deltaText: string | null;
  // "estimated" renders dotted; "unknown" renders "price unavailable".
  costNote?: "estimated" | "unknown" | "partial";
};

function money(v: number, cur: string): string {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: cur || "USD" }).format(v);
  } catch {
    return `${v.toFixed(2)} ${cur}`;
  }
}

function signed(n: number, unit: string, digits = 0): string {
  const s = n > 0 ? "+" : n < 0 ? "−" : "";
  return `${s}${Math.abs(n).toFixed(digits)}${unit}`;
}

export function rows(planned: PvaPlanned, actual: PvaActual): Row[] {
  const cur = planned.currency;
  const cost: Row = {
    key: "cost",
    label: "Cost",
    planned: planned.cost != null ? money(planned.cost, cur) : null,
    actual: actual.cost != null && actual.cost_source !== "unknown" ? money(actual.cost, cur) : null,
    delta: null,
    deltaText: null,
  };
  if (actual.cost == null || actual.cost_source === "unknown") cost.costNote = "unknown";
  else if (actual.unpriced_sessions > 0) cost.costNote = "partial";
  else if (actual.cost_source === "network_estimate" || actual.cost_source === "mixed") cost.costNote = "estimated";
  // A partial total isn't comparable: no delta.
  if (planned.cost != null && actual.cost != null && cost.costNote !== "unknown" && cost.costNote !== "partial") {
    cost.delta = actual.cost - planned.cost;
    cost.deltaText = signed(cost.delta, "", 2).replace(/^([+−]?)/, `$1${currencySymbol(cur)}`);
  }
  const kwh: Row = {
    key: "kwh",
    label: "Energy added",
    planned: planned.kwh != null ? `${planned.kwh.toFixed(0)} kWh` : null,
    actual: actual.kwh != null ? `${actual.kwh.toFixed(0)} kWh` : null,
    delta: planned.kwh != null && actual.kwh != null ? actual.kwh - planned.kwh : null,
    deltaText: null,
  };
  if (kwh.delta != null) kwh.deltaText = signed(kwh.delta, " kWh");
  const stops: Row = {
    key: "stops",
    label: "Charging stops",
    planned: `${planned.stops}`,
    actual: `${actual.stops}`,
    delta: actual.stops - planned.stops,
    deltaText: actual.stops === planned.stops ? null : signed(actual.stops - planned.stops, ""),
  };
  const soc: Row = {
    key: "arrival_soc",
    label: "Arrival battery",
    planned: planned.arrival_soc != null ? `${Math.round(planned.arrival_soc)}%` : null,
    actual: actual.arrival_soc != null ? `${Math.round(actual.arrival_soc)}%` : null,
    delta: planned.arrival_soc != null && actual.arrival_soc != null ? actual.arrival_soc - planned.arrival_soc : null,
    deltaText: null,
  };
  if (soc.delta != null) soc.deltaText = signed(soc.delta, " pts");
  const dur: Row = {
    key: "duration",
    label: "Duration",
    planned: planned.duration_min != null ? hm(planned.duration_min) : null,
    actual: actual.duration_min != null ? hm(actual.duration_min) : null,
    delta: planned.duration_min != null && actual.duration_min != null ? actual.duration_min - planned.duration_min : null,
    deltaText: null,
  };
  if (dur.delta != null) dur.deltaText = signed(dur.delta, " min");
  return [cost, kwh, stops, soc, dur];
}

function currencySymbol(cur: string): string {
  return cur === "USD" || !cur ? "$" : "";
}

export function hm(min: number): string {
  const h = Math.floor(min / 60);
  const m = Math.round(min % 60);
  return h > 0 ? `${h} h ${m} m` : `${m} min`;
}
