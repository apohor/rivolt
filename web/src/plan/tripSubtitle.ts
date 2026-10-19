import type { SavedTripInputs } from "../lib/api";

// The one-line description of a saved or sample trip. A single day is "origin → end"; a multi-day trip
// says it is multi-day, and a round trip reads "origin ↔ farthest stop" instead of naming the same place twice.
const clean = (s: string | undefined): string => (s ?? "").replace(/\s*\(sample\)\s*$/i, "").trim();
const HOTEL = /\b(hotel|inn|suites?|motel|lodge|resort)\b/i;

type Night = { label: string };

function nightsOf(inputs: SavedTripInputs): Night[] {
  const flags = inputs.overnight_flags ?? [];
  return (inputs.extra_stops ?? []).filter((_, i) => !!flags[i]).map((s) => ({ label: s.label }));
}

const sameSpot = (a?: { lat: number; lon: number }, b?: { lat: number; lon: number }): boolean =>
  !!a && !!b && Math.abs(a.lat - b.lat) < 0.0005 && Math.abs(a.lon - b.lon) < 0.0005;

export function tripSubtitle(inputs: SavedTripInputs): string {
  const o = inputs.origin?.label ?? "";
  const d = inputs.destination?.label ?? "";
  const nights = nightsOf(inputs);
  if (nights.length === 0) return `${o} → ${d}`;
  const days = nights.length + 1;
  const origin = clean(o);
  const last = clean(nights[nights.length - 1].label);
  const round = sameSpot(inputs.origin, inputs.destination);
  // Where the trip turns round: the last night's stop. If that is the same place as the start, say so.
  if (round) {
    const far = last && last !== origin ? last : "";
    if (!far) return `${origin} round trip · ${days} days`;
    // One night at the far end is already named by the far end.
    const nightsPart = nights.length > 1 ? ` · ${nights.length} overnights` : "";
    return `${origin} ↔ ${far} · ${days} days${nightsPart}`;
  }
  const end = clean(d);
  const nightsPart = nights.length > 1 ? ` · ${nights.length} overnights` : last ? ` · overnight in ${last}` : "";
  return `${origin} → ${end} · ${days} days${nightsPart}`;
}

// The short tag on a multi-day sample: "2 days · hotel night".
export function tripTag(inputs: SavedTripInputs): string | null {
  const nights = nightsOf(inputs);
  if (nights.length === 0) return null;
  const days = `${nights.length + 1} days`;
  if (nights.length === 1) return `${days} · ${HOTEL.test(nights[0].label) ? "hotel night" : "1 night"}`;
  return `${days} · ${nights.length} nights`;
}

// Multi-day samples go first in a list of samples; the rest keep their order.
export function multiDayFirst<T extends { inputs: SavedTripInputs }>(trips: T[]): T[] {
  return [...trips].sort((a, b) => Number(nightsOf(b.inputs).length > 0) - Number(nightsOf(a.inputs).length > 0));
}
