import { useEffect, useRef } from "react";
import type { TripCostStop, TripRoute } from "../lib/api";
import { formatWaypointTime } from "./format";
import { revealStop } from "./reveal";
import { Unit } from "./Unit";
import { StopPriceNote } from "./StopPrice";
import { stopCostText } from "../lib/planCost";
import { usePreferences } from "../lib/preferences";
import { distanceIn } from "./units";

// StopCard: one itinerary entry for the planner (start, charging
// stop, arrival). A charging stop expands when selected (from here or from the
// map) to show the charger, network, SoC in and out, charge time, energy, power
// and cost. The parent renders these inside <ol data-plan-timeline>.
type Waypoint = TripRoute["Waypoints"][number];

export function StopCard({
  kind,
  name,
  waypoint: w,
  prev,
  stopCost,
  currency,
  timeShiftMs,
  selected,
  onSelect,
  showLeg,
}: {
  kind: "start" | "stop" | "end";
  name: string;
  waypoint: Waypoint;
  prev?: Waypoint;
  stopCost?: TripCostStop;
  currency: string;
  timeShiftMs: number;
  selected: boolean;
  onSelect: () => void;
  showLeg: boolean;
}) {
  const { distanceUnit } = usePreferences();
  const money = (v: number) => new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "USD" }).format(v);
  const arr = formatWaypointTime(w.ArrivalTimeUTC, timeShiftMs, true);
  const dep = formatWaypointTime(w.DepartureTimeUTC, timeShiftMs, true);
  const leg = showLeg && prev ? (w.DistanceFromOriginMeters ?? 0) - (prev.DistanceFromOriginMeters ?? 0) : 0;
  const adapter = w.AdapterRequired && /\bTesla\b|\[Tesla\]/i.test(w.Name);
  const costText = stopCost ? (stopCostText(stopCost, money) ?? "—") : null;
  const inSoc = Math.max(0, Math.min(100, w.ArrivalSoC));
  const outSoc = Math.max(0, Math.min(100, w.DepartureSoC));
  const isStop = kind === "stop";
  const detail = isStop && selected;
  const cardRef = useRef<HTMLLIElement>(null);
  const titleRef = useRef<HTMLButtonElement>(null);
  // When the detail opens (a tap here, or a tap on the stop's map marker),
  // bring the whole card, title included, into view.
  useEffect(() => {
    if (detail && cardRef.current && titleRef.current) revealStop(cardRef.current, titleRef.current);
  }, [detail]);
  return (
    <li ref={cardRef} data-kind={kind} data-selected={selected ? "true" : undefined}>
      <button
        ref={titleRef}
        type="button"
        aria-pressed={selected}
        aria-expanded={isStop ? selected : undefined}
        onClick={onSelect}
        className="block w-full rounded-lg px-3 py-2 text-left transition-colors hover:bg-neutral-900/50"
      >
        <span className="flex items-baseline justify-between gap-3">
          <span className="pt-value min-w-0 text-neutral-100">
            {name}
            {adapter && (
              <span className="ml-2 rounded-sm bg-neutral-800 px-1.5 py-0.5 text-[10px] font-normal uppercase tracking-wide text-neutral-300">adapter</span>
            )}
          </span>
          {leg > 0 && <span className="pt-sub shrink-0 text-neutral-400">+{distanceIn(leg, distanceUnit).toFixed(0)}<Unit>{distanceUnit}</Unit></span>}
        </span>
        <span className="pt-sub mt-0.5 flex flex-wrap items-baseline gap-x-3 text-neutral-400">
          {kind === "start" && (
            <span>
              {dep && <span>{dep}</span>} depart{w.DepartureSoC > 0 && <> at <span className="text-neutral-200">{w.DepartureSoC.toFixed(0)}<Unit>%</Unit></span></>}
            </span>
          )}
          {isStop && (
            <>
              {arr && <span>{arr}</span>}
              <span className="text-neutral-200">{Math.round(w.ChargeDurationSec / 60)}<Unit>min</Unit></span>
              {costText != null && <span className="text-neutral-200">{costText}</span>}
            </>
          )}
          {kind === "end" && (
            <span>
              {arr && <span>{arr}</span>} arrive{w.ArrivalSoC > 0 && <> at <span className="text-neutral-200">{w.ArrivalSoC.toFixed(0)}<Unit>%</Unit></span></>}
            </span>
          )}
        </span>
        {isStop && (
          <span className="pt-sub mt-1.5 flex items-center gap-2 text-neutral-400" data-plan-soc>
            <span className="w-10 shrink-0 text-right">{inSoc.toFixed(0)}<Unit>%</Unit></span>
            <span className="relative h-1.5 flex-1 rounded-full bg-neutral-800/70" aria-hidden>
              <span className="absolute inset-y-0 rounded-full bg-amber-400" style={{ left: `${inSoc}%`, width: `${Math.max(1, outSoc - inSoc)}%` }} />
              <span className="absolute inset-y-0 left-0 rounded-full bg-neutral-600" style={{ width: `${inSoc}%` }} />
            </span>
            <span className="w-10 shrink-0 text-neutral-200">{outSoc.toFixed(0)}<Unit>%</Unit></span>
          </span>
        )}
      </button>
      {detail && (
        <dl data-plan-stop-detail className="pt-sub mx-3 mb-2 mt-1 grid grid-cols-2 gap-x-4 gap-y-2 rounded-lg border border-neutral-800 p-3">
          {stopCost && (
            <div className="col-span-2">
              <dt className="ui-label">Charger</dt>
              <dd className="pt-value text-neutral-100">
                {stopCost.charger_name || w.Name}
                {stopCost.network_name && <span className="text-neutral-400"> · {stopCost.network_name}</span>}
              </dd>
            </div>
          )}
          <div>
            <dt className="ui-label">Arrive</dt>
            <dd className="pt-value text-neutral-100">{arr ? `${arr} · ` : ""}{w.ArrivalSoC.toFixed(0)}<Unit>%</Unit></dd>
          </div>
          <div>
            <dt className="ui-label">Leave</dt>
            <dd className="pt-value text-neutral-100">{dep ? `${dep} · ` : ""}{w.DepartureSoC.toFixed(0)}<Unit>%</Unit></dd>
          </div>
          <div>
            <dt className="ui-label">Charge time</dt>
            <dd className="pt-value text-neutral-100">{Math.round(w.ChargeDurationSec / 60)}<Unit>min</Unit></dd>
          </div>
          {stopCost && (
            <div>
              <dt className="ui-label">Energy</dt>
              <dd className="pt-value text-neutral-100">{stopCost.energy_kwh.toFixed(0)}<Unit>kWh</Unit></dd>
            </div>
          )}
          {w.MaxPowerKW > 0 && (
            <div>
              <dt className="ui-label">Max power</dt>
              <dd className="pt-value text-neutral-100">{w.MaxPowerKW.toFixed(0)}<Unit>kW</Unit></dd>
            </div>
          )}
          {stopCost && (
            <div className="col-span-2">
              <dt className="ui-label">Cost</dt>
              <dd className="pt-value text-neutral-100">
                {costText}
                <StopPriceNote stop={stopCost} money={money} className="pt-sub mt-1 font-normal text-neutral-400" />
              </dd>
            </div>
          )}
          {adapter && (
            <div className="pt-sub col-span-2 text-amber-300">Needs a Tesla NACS adapter at this charger.</div>
          )}
        </dl>
      )}
    </li>
  );
}
