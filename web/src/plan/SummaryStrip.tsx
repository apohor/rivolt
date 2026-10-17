import { ArcGauge } from "../components/ArcGauge";
import type { TripCostEstimate, TripPlan, TripRoute } from "../lib/api";
import { formatDuration, formatWaypointTime } from "./format";
import { Unit } from "./Unit";

// SummaryStrip: the trip at a glance (arrival SoC arc, trip cost with member and
// gas sublines, time, arrival, distance, charging). Used by the ui_theme_next planner.
type Waypoint = TripRoute["Waypoints"][number];

export function SummaryStrip({
  route,
  cost,
  adjustment,
  origin,
  dest,
  timeShiftMs,
  stopCount,
  chargeMinutes,
  compact = false,
}: {
  route: TripRoute;
  cost?: TripCostEstimate;
  adjustment?: TripPlan["weather_adjustment"];
  origin?: Waypoint;
  dest?: Waypoint;
  timeShiftMs: number;
  stopCount: number;
  chargeMinutes: number;
  compact?: boolean;
}) {
  const fmtCurrency = (v: number) =>
    new Intl.NumberFormat(undefined, { style: "currency", currency: cost?.currency || "USD" }).format(v);
  const totalGuest = cost?.dcfc_spend ?? 0;
  const totalUser = cost?.dcfc_spend_user_member ?? 0;
  const userSavings = totalGuest - totalUser;
  const gasEquiv = cost?.gas_equivalent ?? 0;
  const totalChargeMin = chargeMinutes;
  const arrSoCNext = adjustment ? adjustment.final_arrival_soc : route.ArrivalSoC;
  const arriveAt = (() => {
    let t = formatWaypointTime(dest?.ArrivalTimeUTC, timeShiftMs, true);
    if (!t && route.TotalTripDurationSec > 0) {
      const originDep = origin?.DepartureTimeUTC ? new Date(origin.DepartureTimeUTC).getTime() : Date.now();
      t = new Date(Math.round((originDep + route.TotalTripDurationSec * 1000 + timeShiftMs) / 60_000) * 60_000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    }
    return t;
  })();
  // ui_theme_next: the result as a hero (cost, arrival arc, time) over the map and a route timeline.
  return (
    <div data-plan-hero className="mb-4 grid grid-cols-[auto_1fr] items-center gap-x-5 gap-y-4">
      <ArcGauge
        value={arrSoCNext}
        label={arrSoCNext > 0 ? `Arrival battery ${Math.round(arrSoCNext)} percent` : "Arrival battery unknown"}
        tone={arrSoCNext > 0 && arrSoCNext < 15 ? "critical" : "ok"}
        size={compact ? 104 : 116}
      >
        {(shown) => (
          <span className="font-semibold leading-none tabular-nums text-neutral-100">
            <span className="pt-hero">{arrSoCNext > 0 ? Math.round(shown) : "—"}</span>
            <Unit>%</Unit>
          </span>
        )}
      </ArcGauge>
      <div className="min-w-0">
        <p className="ui-label">Trip cost</p>
        <p
          className="pt-hero tabular-nums text-neutral-100"
          title="Walk-up DCFC cost: every fast-charging stop on this route at the network's guest rate."
        >
          {totalGuest > 0 ? fmtCurrency(totalGuest) : "No fast charging"}
        </p>
        {(userSavings > 0.5 || gasEquiv > 0.5) && (
          <p className="mt-1.5 flex flex-wrap gap-x-3 text-xs">
            {userSavings > 0.5 && (
              <span className="text-emerald-400" title="With your active memberships (Settings → Charging networks).">
                <span className="tabular-nums">{fmtCurrency(totalUser)}</span> with memberships
              </span>
            )}
            {gasEquiv > 0.5 && (
              <span className="text-neutral-400" title={`Same distance in a ${cost?.gas_mpg?.toFixed(0) ?? "20"} MPG ICE at ${fmtCurrency(cost?.gas_price_per_gallon ?? 0)}/gal.`}>
                <span className="tabular-nums">{fmtCurrency(gasEquiv)}</span> in gas
              </span>
            )}
          </p>
        )}
      </div>
      <dl className="col-span-2 grid w-full grid-cols-2 gap-x-4 gap-y-3 border-t border-neutral-800 pt-3 text-sm sm:grid-cols-4">
        <div>
          <dt className="ui-label">Time</dt>
          <dd className="pt-value whitespace-nowrap tabular-nums text-neutral-100">{route.TotalTripDurationSec > 0 ? formatDuration(route.TotalTripDurationSec) : "—"}</dd>
        </div>
        <div>
          <dt className="ui-label">Arrive</dt>
          <dd className="pt-value whitespace-nowrap tabular-nums text-neutral-100">{arriveAt || "—"}</dd>
        </div>
        <div>
          <dt className="ui-label">Distance</dt>
          <dd className="pt-value whitespace-nowrap tabular-nums text-neutral-100">{route.TotalDriveDistanceMeters > 0 ? <>{(route.TotalDriveDistanceMeters / 1609.344).toFixed(0)}<Unit>mi</Unit></> : "—"}</dd>
        </div>
        <div>
          <dt className="ui-label">Charging</dt>
          <dd className="pt-value whitespace-nowrap tabular-nums text-neutral-100">
            {stopCount > 0 ? <>{stopCount}<Unit>{stopCount === 1 ? "stop" : "stops"}</Unit> {totalChargeMin}<Unit>min</Unit></> : "none"}
          </dd>
        </div>
      </dl>
    </div>
    );
}
