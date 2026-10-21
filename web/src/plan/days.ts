import type { TripPlanDayRequest, TripPoint } from "../lib/api";
import type { HookupId } from "./hookups";

// Turns the planner form into the day model of POST /api/trips/plan-multiday. The form is an origin, an
// ordered list of stops (each either a via or an overnight) and a destination; an overnight ends one day
// and starts the next. So day k's `end` is the k-th overnight, its vias are the plain stops since the last
// overnight, and the last day ends at the destination with whatever stops follow the final overnight.
export type StopSel = { lat: number; lon: number; label: string };

// stopKey identifies a stop by position, so per-night settings follow the stop through edits and saves.
export const stopKey = (s: { lat: number; lon: number }) => `${s.lat.toFixed(4)},${s.lon.toFixed(4)}`;

export type OvernightOptions = {
  l2Kw: number; // 0: no charger
  hookup?: HookupId; // what the stop has; absent on a save from before hookups (read from the kW)
  campground?: boolean; // the stop is a campground (Recreation.gov): its row says so and carries the data credit
  until: string; // "HH:MM", local time the truck is wanted the next morning
  maxSocPct: number;
};

const point = (s: StopSel): TripPoint => ({ lat: s.lat, lon: s.lon, label: s.label });

// optsFor gives the settings of one overnight stop: each night has its own leave time, charger and charge cap.
export function buildDays(stops: StopSel[], overnightFlags: boolean[], destination: StopSel, optsFor: (s: StopSel) => OvernightOptions, targetSocPct?: number): TripPlanDayRequest[] {
  const days: TripPlanDayRequest[] = [];
  let vias: TripPoint[] = [];
  stops.forEach((s, i) => {
    if (overnightFlags[i]) {
      const o = optsFor(s);
      days.push({
        ...(vias.length ? { vias } : {}),
        end: point(s),
        ...(targetSocPct !== undefined ? { target_arrival_soc_pct: targetSocPct } : {}),
        overnight: { l2_kw: o.l2Kw, until: o.until, max_soc_pct: o.maxSocPct, name: s.label, ...(o.hookup ? { hookup: o.hookup } : {}) },
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
