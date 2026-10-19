import type { SavedTripActive, TripPlanMultidayDay, TripPlanMultidayRequest, TripPlanMultidayResponse } from "../lib/api";
import { totalsOf } from "./plannerHelpers";

// The request that plans the rest of a saved multi-day trip from where the truck is now. `base` is
// the request the trip was planned with (or one rebuilt from the form for an older save).
export function replanRequest(
  base: TripPlanMultidayRequest,
  active: SavedTripActive,
  now: Date = new Date(),
): { req: TripPlanMultidayRequest; fromDay: number; passed: string[]; soc: number | null } | null {
  const overnight = active.phase === "overnight" && !!active.next_day;
  const fromDay = overnight ? active.next_day! : active.current_day;
  if (!fromDay) return null;
  const days = base.days.slice(fromDay - 1).map((d) => ({ ...d }));
  if (days.length === 0) return null;
  // The first day's own departure is in the past; the server starts it from the truck.
  delete days[0].depart_at;
  const passed: string[] = [];
  if (!overnight && days[0].vias?.length) {
    const gone = new Set(active.vias_passed ?? []);
    const kept = days[0].vias.filter((v, i) => {
      if (!gone.has(i + 1)) return true;
      passed.push(v.label ?? "a stop");
      return false;
    });
    if (kept.length) days[0].vias = kept;
    else delete days[0].vias;
  }
  // The truck and its charge are read live: never the values the trip was first planned with.
  const { vehicle_id: _vehicle, starting_soc: _soc, ...rest } = base;
  const projected = overnight ? active.projected_soc : undefined;
  const req: TripPlanMultidayRequest = {
    ...rest,
    days,
    start_at: overnight && active.next_depart_at ? active.next_depart_at : now.toISOString(),
    origin_from_vehicle: true,
    ...(projected !== undefined ? { starting_soc: projected } : {}),
  };
  return { req, fromDay, passed, soc: projected ?? active.position?.soc ?? null };
}

// The saved trip with its days from `fromDay` on replaced by the new plan, numbered as before.
// Earlier days are kept as saved.
export function mergeReplan(
  old: TripPlanMultidayDay[],
  fromDay: number,
  resp: TripPlanMultidayResponse,
): TripPlanMultidayResponse {
  const done = old.filter((d) => d.index < fromDay);
  const fresh = resp.days.map((d, i) => ({ ...d, index: fromDay + i }));
  const days = [...done, ...fresh];
  return {
    days,
    total: totalsOf(days),
    ...(resp.simulated ? { simulated: true } : {}),
    ...(resp.far_future ? { far_future: true } : {}),
    ...(resp.failed_day ? { failed_day: fromDay + resp.failed_day - 1 } : {}),
    ...(resp.error ? { error: resp.error } : {}),
    ...(resp.error_class ? { error_class: resp.error_class } : {}),
    ...(resp.error_reason ? { error_reason: resp.error_reason } : {}),
    ...(resp.from_position ? { from_position: resp.from_position } : {}),
  };
}
