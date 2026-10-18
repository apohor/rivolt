import { Suspense } from "react";
import { type TripPlanMultidayDay, type TripPlanMultidayResponse } from "../lib/api";
import { Card } from "../components/ui";
import { RouteCard } from "./RouteCard";
import { OvernightSuggestions } from "./OvernightSuggestions";
import type { OvernightAdder } from "./tripTypes";
import { Stat } from "./Stat";

// LONG_DAY_THRESHOLD_MI / LONG_DAY_THRESHOLD_HR drive the "long day"
// chip on each day card. Picked from the road-trip rule of thumb that
// driving more than ~10 hours or ~600 miles in a single day is fatigue
// territory.
export const LONG_DAY_THRESHOLD_MI = 600;
export const LONG_DAY_THRESHOLD_HR = 9;

const SIMULATED_NOTE =
  "This plan was built from preview data, not from Rivian's planner. Distances and times are realistic; stops and charge times are estimates for testing.";

const fmtDate = (iso: string) =>
  new Date(iso).toLocaleDateString(undefined, { weekday: "long", month: "short", day: "numeric" });
const fmtTime = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });

// OvernightRow is the night between two days: where, when you get in, and what
// the charger adds. The next day's card starts from it.
export function OvernightRow({ day }: { day: TripPlanMultidayDay }) {
  const o = day.overnight;
  if (!o) return null;
  return (
    <div data-overnight-row className="flex gap-3 rounded-lg border border-dashed border-neutral-700 px-3 py-2.5 text-sm">
      <span aria-hidden className="mt-0.5 text-base leading-none">☾</span>
      <div className="min-w-0 space-y-0.5">
        <div className="font-medium text-neutral-100">Overnight at {o.name || "your stop"}</div>
        <div className="text-neutral-400">
          Arrive about {fmtTime(day.arrive_at)} at {day.arrival_soc.toFixed(0)}%. Parked about {o.parked_hours.toFixed(0)} h.
        </div>
        <div className="text-neutral-400">
          {o.l2_kw > 0 ? (
            <>
              Charging at {o.l2_kw} kW adds about {o.added_kwh.toFixed(0)} kWh → leave at{" "}
              <span className="font-mono text-emerald-400">{o.post_charge_soc_pct.toFixed(0)}%</span>
              {o.capped ? " (capped)" : ""}
            </>
          ) : (
            <>No charger overnight. Leave at {day.arrival_soc.toFixed(0)}%.</>
          )}
        </div>
      </div>
    </div>
  );
}

// MultidayResult renders a multi-day trip as a stack of day cards (each with its
// own map and itinerary) with the overnight between them, under a trip-total
// header. Every day is on the page: the map-first sheet belongs to one route, so
// days are stacked in the page flow instead.
export function MultidayResult({
  response,
  originLabel,
  destLabel,
  departureAt,
  onAddOvernight,
}: {
  response: TripPlanMultidayResponse;
  originLabel: string;
  destLabel: string;
  departureAt?: string;
  onAddOvernight?: OvernightAdder;
}) {
  const totalMiles = response.total.distance_meters / 1609.344;
  const totalHrs = response.total.drive_duration_sec / 3600;
  const chargeMin = Math.round(response.total.charging_duration_sec / 60);
  const failed = response.failed_day;
  const cause = response.error_reason || response.error || "Rivian's planner didn't return a route";
  return (
    <Card title={`${response.days.length}-day trip: ${originLabel || "Origin"} → ${destLabel || "Destination"}`}>
      <dl className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat label="Days" value={String(response.days.length)} />
        <Stat label="Total distance" value={`${totalMiles.toFixed(0)} mi`} />
        <Stat label="Drive time" value={`${totalHrs.toFixed(1)} h`} />
        <Stat label="Charging" value={`${chargeMin} min`} />
      </dl>
      {response.simulated && (
        <div className="mb-3">
          <span
            title={SIMULATED_NOTE}
            className="inline-block rounded-sm bg-amber-900/40 px-1.5 py-0.5 text-xs text-amber-300"
          >
            Simulated
          </span>
          <p className="mt-1 text-xs text-neutral-400">Simulated plan. {SIMULATED_NOTE}</p>
        </div>
      )}
      {failed ? (
        <div role="alert" className="mb-3 rounded-md border border-amber-700/50 bg-amber-950/30 p-3 text-sm text-amber-200">
          <div className="font-semibold">
            {failed > 1 ? `Planned through day ${failed - 1}.` : `Day ${failed} couldn't be planned.`}
          </div>
          <div className="mt-0.5 text-amber-200/80">
            {failed > 1 ? `Day ${failed} failed: ${cause}. Fix day ${failed} and plan again; earlier days are kept.` : `${cause}. Check the stops and plan again.`}
          </div>
        </div>
      ) : null}
      <div className="space-y-3">
        {response.days.map((d, i) => {
          const r = d.plan.Routes?.[0];
          // Per-day origin label = previous day's overnight name, or
          // the trip origin on Day 1. Destination label = this day's
          // overnight name, or the trip destination on the last day.
          const dayOrigin = i === 0
            ? (originLabel || "Origin")
            : (response.days[i - 1].overnight?.name || `Overnight ${i}`);
          const dayDest = d.overnight?.name
            ?? (i === response.days.length - 1 ? (destLabel || "Destination") : `Overnight ${i + 1}`);
          const miles = r ? r.TotalDriveDistanceMeters / 1609.344 : 0;
          const hrs = r ? r.TotalDriveDurationSec / 3600 : 0;
          const isLongDay = miles > LONG_DAY_THRESHOLD_MI || hrs > LONG_DAY_THRESHOLD_HR;
          const stops = r ? r.Waypoints.filter((w) => w.WaypointType.toLowerCase() === "charger").length : 0;
          const short = r && r.BatteryEmptyToDestMeters > 0 ? r.BatteryEmptyToDestMeters / 1609.344 : 0;
          return (
            <div key={d.index} className="space-y-3">
              <div data-day-card className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3">
                <div className="mb-3">
                  <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                    <span className="rounded-sm bg-emerald-900/40 px-1.5 py-0.5 text-xs font-semibold text-emerald-200">
                      Day {d.index}
                    </span>
                    {d.depart_at && <span className="text-sm font-medium text-neutral-100">{fmtDate(d.depart_at)}</span>}
                    {isLongDay && (
                      <span
                        className="rounded-sm bg-amber-900/40 px-1.5 py-0.5 text-xs text-amber-300"
                        title="Over 600 mi or 9 h of driving. Consider an earlier overnight."
                      >
                        long day
                      </span>
                    )}
                  </div>
                  <div className="mt-1 text-sm text-neutral-300">
                    {dayOrigin} → {dayDest}
                  </div>
                  <div className="mt-0.5 text-xs text-neutral-400">
                    {d.depart_at ? `Depart ${fmtTime(d.depart_at)} at ${d.departure_soc.toFixed(0)}%` : `Start at ${d.departure_soc.toFixed(0)}%`}
                    {r && ` · ${miles.toFixed(0)} mi · ${hrs.toFixed(1)} h driving · ${stops} charging stop${stops === 1 ? "" : "s"}`}
                    {d.arrive_at && ` · arrive about ${fmtTime(d.arrive_at)} at ${d.arrival_soc.toFixed(0)}%`}
                  </div>
                </div>
                {d.soc_below_limit && !short && (
                  <p className="mb-2 text-xs text-rose-300">The battery gets very low on this day.</p>
                )}
                {d.destination_reached === false && (
                  <div role="alert" className="mb-3 rounded-md border border-rose-900 bg-rose-950/40 p-2.5 text-sm text-rose-200">
                    <div className="font-semibold">Day {d.index} can't reach {dayDest} on available chargers.</div>
                    <div className="mt-0.5 text-rose-300/80">
                      {short ? `The battery would run out about ${short.toFixed(0)} mi before ${dayDest}. ` : ""}
                      Add an overnight or a via near a fast charger.
                    </div>
                  </div>
                )}
                {r && stops === 0 && (
                  <p className="mb-2 text-xs text-neutral-400">
                    No charging stop needed on this day; you arrive with about {d.arrival_soc.toFixed(0)}%.
                  </p>
                )}
                {r ? (
                  <Suspense
                    fallback={
                      <div className="h-80 animate-pulse rounded-lg border border-neutral-800 bg-neutral-900/50" />
                    }
                  >
                    <RouteCard
                      stacked
                      route={r}
                      index={0}
                      allRoutes={d.plan.Routes ?? [r]}
                      originLabel={dayOrigin}
                      destLabel={dayDest}
                      departureAt={d.depart_at || departureAt}
                      cost={d.costs?.[0]}
                      primaryCost={d.costs?.[0]}
                    />
                  </Suspense>
                ) : (
                  <div className="text-xs text-rose-300">
                    Rivian returned no route for this day.
                  </div>
                )}
              </div>
              {r && !d.overnight && <OvernightSuggestions route={r} day={d.index} onUse={onAddOvernight} />}
              <OvernightRow day={d} />
            </div>
          );
        })}
      </div>
    </Card>
  );
}
