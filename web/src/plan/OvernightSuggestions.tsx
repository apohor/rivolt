import { useQuery } from "@tanstack/react-query";
import { backend, type TripRoute } from "../lib/api";
import { decodePolyline6 } from "../components/TripRouteMap";
import { inDemo } from "../lib/demo";
import { formatDistance, usePreferences } from "../lib/preferences";
import { rangePerHour } from "../lib/chargeRange";
import type { OvernightAdder } from "./tripTypes";
import { StayCard, RidbCredit, OsmCredit } from "./StayCard";
import { CAMP_WARNING, campgroundPageUrl, campPowerLine, fastChargerLine, hookupForConnector, nearestLines, offTargetLine, outletLabel, searchedLine, sitesLine } from "./stays";

// A day that drives longer than this gets a suggestion of where to sleep.
export const SLEEP_AFTER_HOURS = 8;

// OvernightSuggestions: for a long day, hotels with a listed Level 2 charger and campgrounds with a hookup near where the
// driving budget runs out. Listings only: nothing here says a charger is free or working.
export function OvernightSuggestions({ route, day, onUse }: { route: TripRoute; day: number; onUse?: OvernightAdder }) {
  const { distanceUnit } = usePreferences();
  const long = route.TotalDriveDurationSec > SLEEP_AFTER_HOURS * 3600;
  const path = route.route_polyline
    ? decodePolyline6(route.route_polyline)
    : route.Waypoints.map((w) => [w.Latitude, w.Longitude] as [number, number]);
  const q = useQuery({
    queryKey: ["overnight-candidates", route.route_polyline ?? path.length, route.TotalDriveDurationSec],
    queryFn: () => backend.overnightCandidates({ path, drive_sec: route.TotalDriveDurationSec, budget_hours: SLEEP_AFTER_HOURS, kinds: ["hotel", "campground"] }),
    enabled: long && !inDemo() && !!onUse && path.length >= 2,
    retry: 0,
    staleTime: 10 * 60_000,
  });
  const d = q.data;
  if (!d || d.target.reaches_end) return null;
  const aboutIn = formatDistance(d.target.along_m / 1000, distanceUnit);
  const fallback = d.fallback_stays ?? [];
  if (d.candidates.length === 0 && fallback.length > 0) {
    // Hotels with no charger listed (OpenStreetMap): said plainly, with the fast charger the next morning starts at, never styled like a
    // stay that lists one. Using one makes it the night with no charging, and the charger the first stop of the next day.
    const fmtKm = (km: number) => formatDistance(km, distanceUnit);
    return (
      <section data-overnight-suggestions data-overnight-fallback className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3 text-sm" aria-label={`Where to sleep on day ${day}`}>
        <h3 className="font-medium text-neutral-100">Where to sleep on day {day}</h3>
        <p className="mt-0.5 text-xs text-neutral-400">
          About {aboutIn} in. No hotel with a listed charger is in range. These hotels list no charger: you would sleep without charging and start the next day at a fast charger.
        </p>
        <ul className="mt-2 space-y-3">
          {fallback.map((s) => (
            <StayCard
              key={`${s.lat},${s.lon}`}
              name={s.name}
              kind="hotel"
              lines={[s.locality ?? "", <span key="f" data-no-charger className="text-amber-400">{fastChargerLine(s.fast_charger, fmtKm)}</span>]}
              action={
                <button
                  type="button"
                  onClick={() =>
                    onUse?.({
                      lat: s.lat,
                      lon: s.lon,
                      label: s.name,
                      l2Kw: 0,
                      thenVia: { lat: s.fast_charger.lat, lon: s.fast_charger.lon, label: s.fast_charger.name || "Fast charger" },
                    })
                  }
                  aria-label={`Use ${s.name} as the overnight stop, with no charging`}
                  className="shrink-0 rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-200 hover:bg-neutral-800"
                >
                  Use
                </button>
              }
            />
          ))}
        </ul>
        <p className="mt-2 text-xs text-neutral-500">Listings only; availability isn&apos;t checked.</p>
        <OsmCredit attribution={d.attribution} licenceUrl={d.attribution_license_url} sourceUrl={d.attribution_source_url} className="mt-1 text-xs text-neutral-500" />
      </section>
    );
  }
  if (d.candidates.length === 0) {
    return (
      <section data-overnight-suggestions className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3 text-sm" aria-label={`Where to sleep on day ${day}`}>
        <h3 className="font-medium text-neutral-100">Where to sleep on day {day}</h3>
        <p className="mt-0.5 text-xs text-neutral-400">
          About {aboutIn} in. {searchedLine(d.target, { route_m: d.route_m, drive_sec: route.TotalDriveDurationSec }, SLEEP_AFTER_HOURS, (km) => formatDistance(km, distanceUnit))}
        </p>
        {nearestLines(d.nearest).map((l) => (
          <p key={l} className="mt-0.5 text-xs text-neutral-400">{l}</p>
        ))}
      </section>
    );
  }
  const fmtKm = (km: number) => formatDistance(km, distanceUnit);
  const hasCamp = d.candidates.some((c) => c.kind === "campground");
  return (
    <section data-overnight-suggestions className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3 text-sm" aria-label={`Where to sleep on day ${day}`}>
      <h3 className="font-medium text-neutral-100">Where to sleep on day {day}</h3>
      <p className="mt-0.5 text-xs text-neutral-400">
        About {aboutIn} in,{" "}
        {hasCamp ? "these stays list a Level 2 charger or an electric hookup:" : "these hotels list a Level 2 charger:"}
      </p>
      <ul className="mt-2 space-y-3">
        {d.candidates.map((c) => {
          const camp = c.kind === "campground";
          const hookup = hookupForConnector(c.connector);
          const kw = c.planned_kw ?? c.l2_kw;
          const away = `${formatDistance(c.detour_m / 2000, distanceUnit, { fine: true })} off route`;
          const offTarget = offTargetLine(d.target.along_m, c.along_m, SLEEP_AFTER_HOURS, fmtKm);
          const label = camp ? `${c.name} (campground)` : c.name;
          return (
            <StayCard
              key={`${c.lat},${c.lon}`}
              name={c.name}
              kind={camp ? "campground" : "hotel"}
              sample={c.sample}
              lines={
                camp
                  ? [
                      `${outletLabel(hookup)} · ${away}`,
                      offTarget,
                      campPowerLine(hookup, kw),
                      sitesLine(c.electric_sites, c.sites),
                      c.fee ? `Fee: ${c.fee}` : "",
                    ]
                  : [`Level 2 charger · ${rangePerHour(c.l2_kw, distanceUnit)}`, offTarget, `${c.l2_kw_assumed ? `power not listed (${c.l2_kw} kW assumed)` : `up to ${c.l2_kw} kW`} · ${away}`]
              }
              warning={camp ? CAMP_WARNING : undefined}
              link={camp && !c.sample ? campgroundPageUrl(c.source_url) : undefined}
              action={
                <button
                  type="button"
                  onClick={() => onUse?.({ lat: c.lat, lon: c.lon, label, l2Kw: kw, ...(camp ? { hookup, campground: true } : {}) })}
                  aria-label={`Use ${label} as the overnight stop`}
                  className="shrink-0 rounded-md border border-emerald-700/50 bg-emerald-900/30 px-3 py-1.5 text-xs text-emerald-300 hover:bg-emerald-900/50"
                >
                  Use
                </button>
              }
            />
          );
        })}
      </ul>
      <p className="mt-2 text-xs text-neutral-500">Listed chargers only; availability isn&apos;t checked.</p>
      {hasCamp && <RidbCredit className="mt-1 text-xs text-neutral-500" />}
    </section>
  );
}
