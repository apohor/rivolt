import { Suspense } from "react";
import { type TripPlanMultidayResponse } from "../lib/api";
import { Card } from "../components/ui";
import { RouteCard } from "./RouteCard";
import { Stat } from "./Stat";

// LONG_DAY_THRESHOLD_MI / LONG_DAY_THRESHOLD_HR drive the "long day"
// chip on each day card. Picked from the road-trip rule of thumb that
// driving more than ~10 hours or ~600 miles in a single day is fatigue
// territory.
export const LONG_DAY_THRESHOLD_MI = 600;
export const LONG_DAY_THRESHOLD_HR = 9;

// MultidayResult renders a multi-day trip as a stack of full per-day
// RouteCards (with map + stops table) plus a trip-total header. Each
// day carries its own departure/arrival SoC pill, a long-day warning
// when distance or time exceeds the threshold, and an overnight
// footer showing the L2 math. AI advice + saved trips don't flow
// through this path yet.
export function MultidayResult({
  response,
  originLabel,
  destLabel,
  departureAt,
}: {
  response: TripPlanMultidayResponse;
  originLabel: string;
  destLabel: string;
  departureAt?: string;
}) {
  const totalMiles = response.total.distance_meters / 1609.344;
  const totalHrs = response.total.drive_duration_sec / 3600;
  const chargeMin = Math.round(response.total.charging_duration_sec / 60);
  const failed = response.failed_day;
  const fmtDay = (iso: string) =>
    new Date(iso).toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" });
  return (
    <Card title={`${response.days.length}-day trip: ${originLabel || "Origin"} → ${destLabel || "Destination"}`}>
      <div className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat label="Days" value={String(response.days.length)} />
        <Stat label="Total distance" value={`${totalMiles.toFixed(0)} mi`} />
        <Stat label="Drive time" value={`${totalHrs.toFixed(1)} h`} />
        <Stat label="Charging" value={`${chargeMin} min`} />
      </div>
      {response.simulated && (
        <div className="mb-3 inline-block rounded-sm bg-amber-900/40 px-1.5 py-0.5 text-xs text-amber-300">
          Simulated
        </div>
      )}
      {failed ? (
        <div role="alert" className="mb-3 rounded-md border border-amber-700/50 bg-amber-950/30 p-3 text-sm text-amber-200">
          {failed > 1 ? `Planned through day ${failed - 1}. ` : ""}
          Day {failed} couldn't be planned{response.error ? `: ${response.error}` : "."}
        </div>
      ) : null}
      <div className="space-y-4">
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
          return (
            <div key={d.index} className="rounded-lg border border-neutral-800 bg-neutral-950/40 p-3">
              <div className="mb-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
                <span className="rounded-sm bg-emerald-900/40 px-1.5 py-0.5 text-xs font-semibold text-emerald-200">
                  Day {d.index}
                </span>
                {d.depart_at && (
                  <span className="text-xs text-neutral-400">{fmtDay(d.depart_at)}</span>
                )}
                <span className="text-sm text-neutral-300">
                  {dayOrigin} → {dayDest}
                </span>
                <span className="text-xs text-neutral-500">
                  start <span className="text-neutral-300 font-mono">{d.departure_soc.toFixed(0)}%</span>
                  {" → "}
                  arrive <span className="text-neutral-300 font-mono">{d.arrival_soc.toFixed(0)}%</span>
                </span>
                {d.soc_below_limit && (
                  <span className="rounded-sm bg-rose-900/40 px-1.5 py-0.5 text-xs text-rose-300">
                    battery gets very low
                  </span>
                )}
                {d.destination_reached === false && (
                  <span className="rounded-sm bg-rose-900/40 px-1.5 py-0.5 text-xs text-rose-300">
                    doesn't reach the stop
                  </span>
                )}
                {isLongDay && (
                  <span
                    className="rounded-sm bg-amber-900/40 px-1.5 py-0.5 text-xs text-amber-300"
                    title={`Long day: ${miles.toFixed(0)} mi / ${hrs.toFixed(1)} h drive. Consider splitting into two days.`}
                  >
                    long day
                  </span>
                )}
              </div>
              {r ? (
                <Suspense
                  fallback={
                    <div className="h-80 animate-pulse rounded-lg border border-neutral-800 bg-neutral-900/50" />
                  }
                >
                  <RouteCard
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
              {d.overnight && (
                <div className="mt-3 text-xs text-neutral-400">
                  Overnight{" "}
                  {d.overnight.name ? <span className="text-neutral-300">at {d.overnight.name}</span> : null}
                  {" · "}
                  {d.overnight.l2_kw > 0 ? (
                    <>
                      +{d.overnight.added_kwh.toFixed(0)} kWh @ {d.overnight.l2_kw} kW
                      {" · "}
                      depart{" "}
                      <span className="text-emerald-400 font-mono">
                        {d.overnight.post_charge_soc_pct.toFixed(0)}%
                      </span>
                      {d.overnight.capped && (
                        <span className="text-amber-400" title="Hit max overnight SoC cap">
                          {" "}
                          (capped)
                        </span>
                      )}
                    </>
                  ) : (
                    <>no L2 — carry {d.arrival_soc.toFixed(0)}% to next day</>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </Card>
  );
}

