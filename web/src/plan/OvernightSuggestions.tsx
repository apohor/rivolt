import { useQuery } from "@tanstack/react-query";
import { backend, type TripRoute } from "../lib/api";
import { decodePolyline6 } from "../components/TripRouteMap";
import { inDemo } from "../lib/demo";
import { formatDistance, usePreferences } from "../lib/preferences";
import type { OvernightAdder } from "./tripTypes";

// A day that drives longer than this gets a suggestion of where to sleep.
export const SLEEP_AFTER_HOURS = 8;

// OvernightSuggestions: for a long day, hotels with a listed Level 2 charger near where the
// driving budget runs out. Listings only: nothing here says a charger is free or working.
export function OvernightSuggestions({ route, day, onUse }: { route: TripRoute; day: number; onUse?: OvernightAdder }) {
  const { distanceUnit } = usePreferences();
  const long = route.TotalDriveDurationSec > SLEEP_AFTER_HOURS * 3600;
  const path = route.route_polyline
    ? decodePolyline6(route.route_polyline)
    : route.Waypoints.map((w) => [w.Latitude, w.Longitude] as [number, number]);
  const q = useQuery({
    queryKey: ["overnight-candidates", route.route_polyline ?? path.length, route.TotalDriveDurationSec],
    queryFn: () => backend.overnightCandidates({ path, drive_sec: route.TotalDriveDurationSec, budget_hours: SLEEP_AFTER_HOURS }),
    enabled: long && !inDemo() && !!onUse && path.length >= 2,
    retry: 0,
    staleTime: 10 * 60_000,
  });
  const d = q.data;
  if (!d || d.target.reaches_end || d.candidates.length === 0) return null;
  return (
    <section data-overnight-suggestions className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3 text-sm" aria-label={`Where to sleep on day ${day}`}>
      <h3 className="font-medium text-neutral-100">Where to sleep on day {day}</h3>
      <p className="mt-0.5 text-xs text-neutral-400">
        About {formatDistance(d.target.along_m / 1000, distanceUnit)} in, these hotels list a Level 2 charger:
      </p>
      <ul className="mt-2 space-y-2">
        {d.candidates.map((c) => (
          <li key={`${c.lat},${c.lon}`} className="flex items-center gap-3">
            <div className="min-w-0 flex-1">
              <div className="truncate text-neutral-100">{c.name}</div>
              <div className="text-xs text-neutral-400">
                {c.l2_kw_assumed ? `power not listed (${c.l2_kw} kW assumed)` : `up to ${c.l2_kw} kW`} ·{" "}
                {formatDistance(c.detour_m / 2000, distanceUnit, { fine: true })} off route
              </div>
            </div>
            <button
              type="button"
              onClick={() => onUse?.({ lat: c.lat, lon: c.lon, label: c.name, l2Kw: c.l2_kw })}
              aria-label={`Use ${c.name} as the overnight stop`}
              className="shrink-0 rounded-md border border-emerald-700/50 bg-emerald-900/30 px-3 py-1.5 text-xs text-emerald-300 hover:bg-emerald-900/50"
            >
              Use
            </button>
          </li>
        ))}
      </ul>
      <p className="mt-2 text-xs text-neutral-500">Listed chargers only; availability isn&apos;t checked.</p>
    </section>
  );
}
