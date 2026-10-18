import type { TripPlanDayRequest, TripPoint } from "../lib/api";

// Turns the planner form into the day model of POST /api/trips/plan-multiday. The form is an origin, an
// ordered list of stops (each either a via or an overnight) and a destination; an overnight ends one day
// and starts the next. So day k's `end` is the k-th overnight, its vias are the plain stops since the last
// overnight, and the last day ends at the destination with whatever stops follow the final overnight.
export type StopSel = { lat: number; lon: number; label: string };

export type OvernightOptions = {
  l2Kw: number; // 0: no charger
  until: string; // "HH:MM", local time the truck is wanted the next morning
  maxSocPct: number;
};

const point = (s: StopSel): TripPoint => ({ lat: s.lat, lon: s.lon, label: s.label });

export function buildDays(stops: StopSel[], overnightFlags: boolean[], destination: StopSel, o: OvernightOptions, targetSocPct?: number): TripPlanDayRequest[] {
  const days: TripPlanDayRequest[] = [];
  let vias: TripPoint[] = [];
  stops.forEach((s, i) => {
    if (overnightFlags[i]) {
      days.push({
        ...(vias.length ? { vias } : {}),
        end: point(s),
        ...(targetSocPct !== undefined ? { target_arrival_soc_pct: targetSocPct } : {}),
        overnight: { l2_kw: o.l2Kw, until: o.until, max_soc_pct: o.maxSocPct, name: s.label },
      });
      vias = [];
    } else {
      vias.push(point(s));
    }
  });
  days.push({
    ...(vias.length ? { vias } : {}),
    end: point(destination),
    ...(targetSocPct !== undefined ? { target_arrival_soc_pct: targetSocPct } : {}),
  });
  return days;
}

// The overnight stops' chargers the form offers (growth's copy), in kW.
export const OVERNIGHT_CHARGERS: { kw: number; label: string }[] = [
  { kw: 7, label: "Level 2 (7 kW)" },
  { kw: 11, label: "Level 2, fast (11 kW)" },
  { kw: 1.4, label: "Slow outlet (1.4 kW)" },
  { kw: 0, label: "No charger" },
];
