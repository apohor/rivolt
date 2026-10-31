import { lazy, Suspense, useEffect, useState } from "react";
import { StopCard } from "./StopCard";
import { SummaryStrip } from "./SummaryStrip";
import { formatDuration, formatWaypointTime } from "./format";
import { CostEstimateNote } from "./CostEstimateNote";
import { BottomSheet } from "./BottomSheet";
import { useSelectedStop } from "./selection";
import { formatDistance, usePreferences } from "../lib/preferences";
import { wholeDistance } from "./units";
import { type TripCostEstimate, type TripPlan, type TripRoute } from "../lib/api";
import { Card } from "../components/ui";
import { OvernightAdder, StopAdder, StopPreviewer } from "./tripTypes";
import { isChargingWaypoint } from "./waypoints";
import { routeDiff, routeLabel } from "./RouteChooser";
import { StopPriceNote, stopCostOrDash } from "./StopPrice";
import { estimateLabel, fmtRange, spendView } from "../lib/planCost";
import { Stat } from "./Stat";

// Lazy-load TripRouteMap so the MapLibre
// chunk (several hundred KB) only ships when the user actually opens
// a plan with a rendered map, not on every page navigation.
const TripRouteMap = lazy(() =>
  import("../map/TripRouteMap").then((m) => ({ default: m.TripRouteMap })),
);


export function RouteCard({
  route,
  index,
  allRoutes,
  originLabel,
  destLabel,
  onAddStop,
  onAddOvernight,
  onPreviewStop,
  departureAt,
  cost,
  primaryCost,
  adjustment,
  defaultCollapsed,
  forceNext = false,
  stacked = false,
  chooser,
  dayLabel,
}: {
  route: TripRoute;
  index: number;
  allRoutes: TripRoute[];
  originLabel: string;
  destLabel: string;
  // "Day 2" on a multi-day plan: the map's legend card says which day its legs belong to.
  dayLabel?: string;
  onAddStop?: StopAdder;
  onAddOvernight?: OvernightAdder;
  onPreviewStop?: StopPreviewer;
  departureAt?: string;
  cost?: TripCostEstimate;
  primaryCost?: TripCostEstimate;
  adjustment?: TripPlan["weather_adjustment"];
  // Collapse the body (map + stops table + per-network breakdown)
  // behind a "Tap to view" affordance on first render. Caller sets
  // this for alternatives so the page doesn't pile maps on top of
  // each other before the user signals interest. The Recommended
  // route stays expanded.
  defaultCollapsed?: boolean;
  // Render this (alternative) route in the map-first layout.
  forceNext?: boolean;
  // One of several routes stacked on the page (a day of a multi-day trip):
  // always the in-flow card, never the map-first sheet, which only one route can own.
  stacked?: boolean;
  // The route chooser, shown at the top of the summary.
  chooser?: React.ReactNode;
}) {
  const { distanceUnit } = usePreferences();
  // Map-first layout is for the primary route; alternatives and stacked days keep the in-flow card.
  const next = !stacked && (index === 0 || forceNext);
  const [expanded, setExpanded] = useState(!defaultCollapsed);
  // Rivian's planTrip2 returns waypointType in lowercase ("origin" /
  // "destination" / "waypoint"); compare case-insensitively so the
  // table renders correctly regardless of casing.
  const wpType = (w: { WaypointType: string }) => w.WaypointType.toLowerCase();
  // Track each charging stop's original index in route.Waypoints so
  // distanceCell can look up the predecessor for leg-distance math.
  const chargingWithIdx = route.Waypoints
    .map((w, i) => ({ w, i }))
    .filter(({ w }) => isChargingWaypoint(w));
  const charging = chargingWithIdx.map(({ w }) => w);
  const originIdx = route.Waypoints.findIndex((w) => wpType(w) === "origin");
  const destIdx = (() => {
    // Avoid findLastIndex - target lib predates ES2023. Walk in
    // reverse so multi-segment routes still find the trailing
    // destination row.
    for (let i = route.Waypoints.length - 1; i >= 0; i--) {
      if (wpType(route.Waypoints[i]) === "destination") return i;
    }
    return -1;
  })();
  const origin = originIdx >= 0 ? route.Waypoints[originIdx] : undefined;
  const dest = destIdx >= 0 ? route.Waypoints[destIdx] : undefined;
  const totalChargeMin = Math.round(route.TotalChargingDurationSec / 60);
  const showTable = origin || dest || charging.length > 0;

  // Shift displayed times so that the origin departure matches the
  // user's chosen departure datetime. Rivian plans from "now", so
  // the delta is (userDeparture - originDepartureTimeUTC).
  const timeShiftMs = (() => {
    if (!departureAt) return 0;
    const userDep = new Date(departureAt).getTime();
    const rivianDep = origin?.DepartureTimeUTC
      ? new Date(origin.DepartureTimeUTC).getTime()
      : Date.now();
    return userDep - rivianDep;
  })();
  const fmtCurrency = (v: number) =>
    new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: cost?.currency || "USD",
    }).format(v);
  // Per-stop $ keyed by the same index the stops table walks. The
  // breakdown is server-computed from the same Waypoints array, so
  // index alignment is safe.
  const stopBreakdown = cost?.breakdown ?? [];
  const spend = spendView(cost);
  const gasEquiv = cost?.gas_equivalent ?? 0;
  // Bidirectional selection between the stops table and the map.
  // Holds the index in route.Waypoints of the highlighted stop, or
  // null for "nothing selected". Reset to null when the route prop
  // changes so an old selection doesn't leak across plans.
  // Shared with the rest of the map-first layout through
  // PlanSelectionProvider; the classic cards keep their own (see useSelectedStop).
  const stopSel = useSelectedStop(route, { shared: next });
  const selectedStop = stopSel.selected;
  const setSelectedStop = stopSel.select;
  const toggleStop = stopSel.toggle;
  // The route chooser swaps which route this card shows; the shared selection
  // belongs to one route's stops, so a different route starts with none.
  const clearStop = stopSel.clear;
  useEffect(() => {
    if (next) clearStop();
  }, [route, next, clearStop]);
  // Distance helpers. cumulative is route.Waypoints[i].DistanceFromOriginMeters;
  // leg = current - previous. Hidden when Rivian didn't populate the
  // field (legacy plans). All values converted to miles for display.
  const haveDistances = route.Waypoints.some((w) => (w.DistanceFromOriginMeters ?? 0) > 0);
  const mFmt = (m: number) => wholeDistance(m, distanceUnit);
  const distanceCell = (idx: number) => {
    const w = route.Waypoints[idx];
    const cum = w?.DistanceFromOriginMeters ?? 0;
    if (idx === 0) {
      return <td className="px-2 py-2 font-mono text-xs text-neutral-500">0 {distanceUnit}</td>;
    }
    const prev = route.Waypoints[idx - 1];
    const leg = cum - (prev?.DistanceFromOriginMeters ?? 0);
    return (
      <td className="px-2 py-2 font-mono text-neutral-200">
        {leg > 0 ? `+${mFmt(leg)}` : "—"}
        {cum > 0 && (
          <div className="text-xs text-neutral-500">{mFmt(cum)} total</div>
        )}
      </td>
    );
  };
  const label = routeLabel(index, allRoutes);
  // Title is JSX so the member price can render in green inline
  // without a verbal "with Membership" suffix - color carries the
  // meaning. Falls back to a single number when no savings exist.
  const title = (
    <span className="flex flex-wrap items-baseline gap-x-2">
      <span>{label}</span>
      {spend.kind === "range" && (
        <span className="text-neutral-400">
          · <span
            className="text-neutral-200 font-mono cursor-help"
            title="Fast-charging stops on this route, at the price shown on each stop."
          >{fmtRange(spend.low, spend.high, fmtCurrency)}</span>
          {gasEquiv > 0.5 && (
            <>
              {" / "}
              <span
                className="text-rose-400 font-mono cursor-help"
                title={`Same distance in a ${cost?.gas_mpg?.toFixed(0) ?? "20"} MPG ICE at ${fmtCurrency(cost?.gas_price_per_gallon ?? 0)}/gal (Settings → Trip planner defaults).`}
              >{fmtCurrency(gasEquiv)}</span>
            </>
          )}
        </span>
      )}
      {!route.DestinationReached && (
        <span className="text-amber-400">· destination unreachable</span>
      )}
    </span>
  );
  const hero = (
    <SummaryStrip
      route={route}
      cost={cost}
      adjustment={adjustment}
      origin={origin}
      dest={dest}
      timeShiftMs={timeShiftMs}
      stopCount={charging.length}
      chargeMinutes={totalChargeMin}
      compact={next || stacked}
    />
  );
  const row = (idx: number, kind: "start" | "stop" | "end", name: string, n?: number) => (
    <StopCard
      key={idx}
      kind={kind}
      name={name}
      waypoint={route.Waypoints[idx]}
      prev={idx > 0 ? route.Waypoints[idx - 1] : undefined}
      showLeg={haveDistances}
      stopCost={kind === "stop" && n != null ? stopBreakdown[n] : undefined}
      currency={cost?.currency || "USD"}
      timeShiftMs={timeShiftMs}
      selected={selectedStop === idx}
      onSelect={() => toggleStop(idx)}
    />
  );
  const timeline = showTable && (
    <ol data-plan-timeline aria-label="Route and charging stops" className="mt-4">
      {origin && row(originIdx, "start", originLabel || origin.Name || "Origin")}
      {chargingWithIdx.map(({ w, i: wpIdx }, j) => row(wpIdx, "stop", w.Name || `(${w.Latitude.toFixed(3)}, ${w.Longitude.toFixed(3)})`, j))}
      {dest && row(destIdx, "end", destLabel || dest.Name || "Destination")}
    </ol>
  );
  if (next) {
    // Map-first: summary strip and itinerary in the left panel, the map beside it.
    return (
      <>
        {/* On a phone the summary peeks over the map and the itinerary opens
            with it; from 1024px up BottomSheet renders the two inline, as grid
            items beside the map. */}
        <BottomSheet
          label="Trip details"
          peekPx={240}
          peek={
            <section data-plan-area="summary" aria-label="Trip summary">
              {chooser}
              {!route.DestinationReached && (
                <p className="mb-3 text-sm text-amber-400">
                  Destination unreachable
                  {route.BatteryEmptyToDestMeters > 0 && <> — battery empty {formatDistance(route.BatteryEmptyToDestMeters / 1000, distanceUnit, { fine: true })} short</>}
                </p>
              )}
              {hero}
            </section>
          }
        >
          <section data-plan-area="itinerary" aria-label="Itinerary">
            <h2 className="ui-label mb-1">Itinerary</h2>
            {timeline}
          </section>
        </BottomSheet>
        <div data-plan-area="map">
          <Suspense fallback={<div className="h-full animate-pulse bg-neutral-900/50" />}>
            <TripRouteMap
              fill
              route={route}
              onAddStop={onAddStop}
              onAddOvernight={onAddOvernight}
              onPreviewStop={onPreviewStop}
              selectedIdx={selectedStop}
              onSelectStop={(idx) => setSelectedStop(idx)}
              departureAt={departureAt}
              dayLabel={dayLabel}
              originLabel={originLabel}
              destLabel={destLabel}
            />
          </Suspense>
        </div>
      </>
    );
  }
  return (
    <Card title={title}>
      {spend.kind === "range" && (
        spend.older ? (
          <CostEstimateNote className="-mt-1 mb-3 text-[11px] text-neutral-500" />
        ) : (
          <p data-cost-note className="-mt-1 mb-3 text-[11px] text-neutral-500">
            {cost ? estimateLabel(cost) : "estimate"}
            {spend.unpriced > 0 && <> · {spend.priced} of {spend.priced + spend.unpriced} stops priced; the rest show their price at the charger</>}
          </p>
        )
      )}
      {spend.kind === "unpriced" && <p data-cost-note className="-mt-1 mb-3 text-[11px] text-neutral-500">Price shown at the charger.</p>}
      {index > 0 && allRoutes.length > 1 && (() => {
        // Alternative comparison line. Renders below the title so the
        // user can see at a glance how this route diverges from
        // Recommended (route 0) - "saves $12 · 18 min slower · 1
        // fewer stop". Dropped entirely when no dimension crossed
        // the noise threshold.
        const parts = routeDiff(route, cost, allRoutes[0], primaryCost);
        if (parts.length === 0) return null;
        return (
          <div className="-mt-1 mb-3 text-xs text-neutral-400">
            vs Recommended: {parts.join(" · ")}
          </div>
        );
      })()}
      {/* Collapse toggle for alternatives. Mirrors TripAdviceCard's
          pattern: same row label flips between "Tap to view" and
          "Tap to collapse" so the affordance never moves under the
          cursor. Hidden entirely for the Recommended route (which
          never starts collapsed). */}
      {defaultCollapsed && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="mb-3 flex items-center gap-2 text-sm text-left rounded-md w-full hover:bg-neutral-900/50 transition-colors py-1 -my-1 px-1"
        >
          <span className="text-neutral-300">Map + stops</span>
          <span className="ml-auto text-xs text-neutral-500">
            {expanded ? "Tap to collapse" : "Tap to view"}
          </span>
        </button>
      )}
      {expanded && (
      <>
      <div className="mb-4">
        <Suspense fallback={<div className="h-80 animate-pulse rounded-lg border border-neutral-800 bg-neutral-900/50" />}>
          <TripRouteMap
            route={route}
            onAddStop={onAddStop}
            onAddOvernight={onAddOvernight}
            onPreviewStop={onPreviewStop}
            selectedIdx={selectedStop}
            onSelectStop={(idx) => setSelectedStop(idx)}
            departureAt={departureAt}
            dayLabel={dayLabel}
            originLabel={originLabel}
            destLabel={destLabel}
          />
        </Suspense>
      </div>
      <dl className="grid grid-cols-2 gap-y-2 gap-x-6 text-sm sm:grid-cols-4">
        <Stat
          label="Distance"
          value={route.TotalDriveDistanceMeters > 0
            ? wholeDistance(route.TotalDriveDistanceMeters, distanceUnit)
            : "—"}
        />
        <Stat
          label="Total time"
          value={route.TotalTripDurationSec > 0
            ? formatDuration(route.TotalTripDurationSec)
            : "—"}
        />
        <Stat
          label="Arrival"
          value={
            (() => {
              // Prefer Rivian's per-waypoint arrival time when present.
              // Fall back to (departure + totalTripDurationSec) so the
              // stat always reads something useful — Rivian sometimes
              // omits arrivalTimeUTC on the destination waypoint.
              let t = formatWaypointTime(dest?.ArrivalTimeUTC, timeShiftMs);
              if (!t && route.TotalTripDurationSec > 0) {
                const originDep = origin?.DepartureTimeUTC
                  ? new Date(origin.DepartureTimeUTC).getTime()
                  : Date.now();
                const arriveMs = originDep + route.TotalTripDurationSec * 1000 + timeShiftMs;
                t = new Date(arriveMs).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
              }
              // Prefer the corrected arrival SoC when the
              // adjustment pipeline ran (weather and/or profile);
              // it's the realistic number the user will actually
              // see at the destination, not Rivian's
              // weather/config-blind projection.
              const arrSoC = adjustment ? adjustment.final_arrival_soc : route.ArrivalSoC;
              const soc = arrSoC > 0 ? `${arrSoC.toFixed(0)}%` : "—";
              return t ? `${t} · ${soc}` : soc;
            })()
          }
        />
        <Stat
          label="Charging"
          value={
            charging.length > 0
              ? `${charging.length} stop${charging.length === 1 ? "" : "s"} · ${totalChargeMin} min`
              : "0 stops"
          }
        />
      </dl>
      {stacked && timeline}
      {showTable && !stacked && (
        <div className="mt-4 overflow-x-auto" tabIndex={0} role="region" aria-label="Charging stops table">
          <table className="w-full text-sm">
            <thead className="border-b border-neutral-800 text-left text-xs uppercase tracking-wide text-neutral-500">
              <tr>
                <th className="px-2 py-2">#</th>
                <th className="px-2 py-2">Stop</th>
                {haveDistances && <th className="px-2 py-2">Distance</th>}
                <th className="px-2 py-2">Arrive</th>
                <th className="px-2 py-2">Depart</th>
                <th className="px-2 py-2">Charge</th>
                <th className="px-2 py-2">Max kW</th>
                {stopBreakdown.length > 0 && <th className="px-2 py-2 text-right">Cost</th>}
                <th className="px-2 py-2">Adapter?</th>
              </tr>
            </thead>
            <tbody>
              {origin && (
                <tr
                  onClick={() => toggleStop(originIdx)}
                  className={`border-b border-neutral-900 text-neutral-400 cursor-pointer transition-colors ${
                    selectedStop === originIdx ? "bg-emerald-900/20" : "hover:bg-neutral-900/40"
                  }`}
                >
                  <td className="px-2 py-2">S</td>
                  <td className="px-2 py-2">{originLabel || origin.Name || "Origin"}</td>
                  {haveDistances && distanceCell(originIdx)}
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2 font-mono text-neutral-100">
                    {formatWaypointTime(origin.DepartureTimeUTC, timeShiftMs) && (
                      <div className="text-xs text-neutral-500">{formatWaypointTime(origin.DepartureTimeUTC, timeShiftMs)}</div>
                    )}
                    {origin.DepartureSoC > 0 ? `${origin.DepartureSoC.toFixed(0)}%` : "—"}
                  </td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2">—</td>
                  {stopBreakdown.length > 0 && <td className="px-2 py-2"></td>}
                  <td className="px-2 py-2"></td>
                </tr>
              )}
              {chargingWithIdx.map(({ w, i: wpIdx }, j) => {
                const stopCost = stopBreakdown[j];
                return (
                  <tr
                    key={j}
                    onClick={() => toggleStop(wpIdx)}
                    className={`border-b border-neutral-900 cursor-pointer transition-colors ${
                      selectedStop === wpIdx ? "bg-emerald-900/20" : "hover:bg-neutral-900/40"
                    }`}
                  >
                    <td className="px-2 py-2 text-neutral-500">{j + 1}</td>
                    <td className="px-2 py-2">{w.Name || `(${w.Latitude.toFixed(3)}, ${w.Longitude.toFixed(3)})`}</td>
                    {haveDistances && distanceCell(wpIdx)}
                    <td className="px-2 py-2 font-mono">
                      {formatWaypointTime(w.ArrivalTimeUTC, timeShiftMs) && (
                        <div className="text-xs text-neutral-500">{formatWaypointTime(w.ArrivalTimeUTC, timeShiftMs)}</div>
                      )}
                      {w.ArrivalSoC.toFixed(0)}%
                    </td>
                    <td className="px-2 py-2 font-mono">
                      {formatWaypointTime(w.DepartureTimeUTC, timeShiftMs) && (
                        <div className="text-xs text-neutral-500">{formatWaypointTime(w.DepartureTimeUTC, timeShiftMs)}</div>
                      )}
                      {w.DepartureSoC.toFixed(0)}%
                    </td>
                    <td className="px-2 py-2 font-mono">{Math.round(w.ChargeDurationSec / 60)} min</td>
                    <td className="px-2 py-2 font-mono">{w.MaxPowerKW > 0 ? w.MaxPowerKW.toFixed(0) : "—"}</td>
                    {stopBreakdown.length > 0 && (
                      <td className="px-2 py-2 font-mono text-right">
                        <div>{stopCostOrDash(stopCost, fmtCurrency)}</div>
                        <StopPriceNote stop={stopCost} money={fmtCurrency} className="mt-0.5 text-left text-[11px] font-sans text-neutral-500" />
                      </td>
                    )}
                    <td className="px-2 py-2">
                      {w.AdapterRequired && /\bTesla\b|\[Tesla\]/i.test(w.Name) ? "yes" : ""}
                    </td>
                  </tr>
                );
              })}
              {dest && (
                <tr
                  onClick={() => toggleStop(destIdx)}
                  className={`border-b border-neutral-900 text-neutral-400 cursor-pointer transition-colors ${
                    selectedStop === destIdx ? "bg-emerald-900/20" : "hover:bg-neutral-900/40"
                  }`}
                >
                  <td className="px-2 py-2">E</td>
                  <td className="px-2 py-2">{destLabel || dest.Name || "Destination"}</td>
                  {haveDistances && distanceCell(destIdx)}
                  <td className="px-2 py-2 font-mono text-neutral-100">
                    {formatWaypointTime(dest.ArrivalTimeUTC, timeShiftMs) && (
                      <div className="text-xs text-neutral-500">{formatWaypointTime(dest.ArrivalTimeUTC, timeShiftMs)}</div>
                    )}
                    {dest.ArrivalSoC > 0 ? `${dest.ArrivalSoC.toFixed(0)}%` : "—"}
                  </td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2">—</td>
                  <td className="px-2 py-2">—</td>
                  {stopBreakdown.length > 0 && (
                    <td className="px-2 py-2 font-mono text-right text-neutral-200">
                      {spend.kind === "range" ? fmtRange(spend.low, spend.high, fmtCurrency) : "—"}
                    </td>
                  )}
                  <td className="px-2 py-2"></td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
      {!route.DestinationReached && route.BatteryEmptyToDestMeters > 0 && (
        <p className="mt-3 text-sm text-amber-400">
          Battery empty {formatDistance(route.BatteryEmptyToDestMeters / 1000, distanceUnit, { fine: true })} short of destination
          {route.BatteryEmptyLat !== 0 && (
            <>
              {" "}at ({route.BatteryEmptyLat.toFixed(3)}, {route.BatteryEmptyLon.toFixed(3)})
            </>
          )}
          .
        </p>
      )}
      </>
      )}
    </Card>
  );
}

