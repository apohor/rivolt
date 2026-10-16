import type { TripCostStop, TripRoute } from "../lib/api";
import { formatWaypointTime } from "./format";

// StopCard: one itinerary entry for the ui_theme_next planner (start, charging
// stop, arrival). A charging stop expands when selected (from here or from the
// map) to show the charger, network, SoC in and out, charge time, energy, power
// and cost. The parent renders these inside <ol data-plan-timeline>.
type Waypoint = TripRoute["Waypoints"][number];

const mi = (m: number) => `${(m / 1609.344).toFixed(0)} mi`;

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
  const money = (v: number) => new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "USD" }).format(v);
  const arr = formatWaypointTime(w.ArrivalTimeUTC, timeShiftMs);
  const dep = formatWaypointTime(w.DepartureTimeUTC, timeShiftMs);
  const leg = showLeg && prev ? (w.DistanceFromOriginMeters ?? 0) - (prev.DistanceFromOriginMeters ?? 0) : 0;
  const adapter = w.AdapterRequired && /\bTesla\b|\[Tesla\]/i.test(w.Name);
  const guest = stopCost ? stopCost.energy_kwh * stopCost.guest_rate : null;
  const member = stopCost && stopCost.user_rate < stopCost.guest_rate ? stopCost.energy_kwh * stopCost.user_rate : null;
  const inSoc = Math.max(0, Math.min(100, w.ArrivalSoC));
  const outSoc = Math.max(0, Math.min(100, w.DepartureSoC));
  const isStop = kind === "stop";
  const detail = isStop && selected;
  return (
    <li data-kind={kind} data-selected={selected ? "true" : undefined}>
      <button
        type="button"
        aria-pressed={selected}
        aria-expanded={isStop ? selected : undefined}
        onClick={onSelect}
        className="block w-full rounded-lg px-3 py-2 text-left transition-colors hover:bg-neutral-900/50"
      >
        <span className="flex items-baseline justify-between gap-3">
          <span className="min-w-0 text-sm font-medium text-neutral-100">
            {name}
            {adapter && (
              <span className="ml-2 rounded-sm bg-neutral-800 px-1.5 py-0.5 text-[10px] font-normal uppercase tracking-wide text-neutral-300">adapter</span>
            )}
          </span>
          {leg > 0 && <span className="ui-mono shrink-0 text-xs text-neutral-500">+{mi(leg)}</span>}
        </span>
        <span className="mt-0.5 flex flex-wrap items-baseline gap-x-3 text-xs text-neutral-400">
          {kind === "start" && (
            <span>
              {dep && <span className="ui-mono">{dep}</span>} depart{w.DepartureSoC > 0 && <> at <span className="ui-mono text-neutral-200">{w.DepartureSoC.toFixed(0)}%</span></>}
            </span>
          )}
          {isStop && (
            <>
              {arr && <span className="ui-mono">{arr}</span>}
              <span className="ui-mono text-neutral-200">{Math.round(w.ChargeDurationSec / 60)} min</span>
              {guest != null && (
                <span className="ui-mono text-neutral-200">
                  {money(guest)}
                  {member != null && <span className="ml-1 text-emerald-400">{money(member)}</span>}
                </span>
              )}
            </>
          )}
          {kind === "end" && (
            <span>
              {arr && <span className="ui-mono">{arr}</span>} arrive{w.ArrivalSoC > 0 && <> at <span className="ui-mono text-neutral-200">{w.ArrivalSoC.toFixed(0)}%</span></>}
            </span>
          )}
        </span>
        {isStop && (
          <span className="mt-1.5 flex items-center gap-2 text-[11px] text-neutral-400" data-plan-soc>
            <span className="ui-mono w-8 shrink-0 text-right">{inSoc.toFixed(0)}%</span>
            <span className="relative h-1.5 flex-1 rounded-full bg-neutral-800/70" aria-hidden>
              <span className="absolute inset-y-0 rounded-full bg-amber-400" style={{ left: `${inSoc}%`, width: `${Math.max(1, outSoc - inSoc)}%` }} />
              <span className="absolute inset-y-0 left-0 rounded-full bg-neutral-600" style={{ width: `${inSoc}%` }} />
            </span>
            <span className="ui-mono w-8 shrink-0 text-neutral-200">{outSoc.toFixed(0)}%</span>
          </span>
        )}
      </button>
      {detail && (
        <dl data-plan-stop-detail className="mx-3 mb-2 mt-1 grid grid-cols-2 gap-x-4 gap-y-2 rounded-lg border border-neutral-800 p-3 text-xs">
          {stopCost && (
            <div className="col-span-2">
              <dt className="ui-label">Charger</dt>
              <dd className="text-sm text-neutral-100">
                {stopCost.charger_name || w.Name}
                {stopCost.network_name && <span className="text-neutral-400"> · {stopCost.network_name}</span>}
              </dd>
            </div>
          )}
          <div>
            <dt className="ui-label">Arrive</dt>
            <dd className="ui-mono text-neutral-100">{arr ? `${arr} · ` : ""}{w.ArrivalSoC.toFixed(0)}%</dd>
          </div>
          <div>
            <dt className="ui-label">Leave</dt>
            <dd className="ui-mono text-neutral-100">{dep ? `${dep} · ` : ""}{w.DepartureSoC.toFixed(0)}%</dd>
          </div>
          <div>
            <dt className="ui-label">Charge time</dt>
            <dd className="ui-mono text-neutral-100">{Math.round(w.ChargeDurationSec / 60)} min</dd>
          </div>
          {stopCost && (
            <div>
              <dt className="ui-label">Energy</dt>
              <dd className="ui-mono text-neutral-100">{stopCost.energy_kwh.toFixed(0)} kWh</dd>
            </div>
          )}
          {w.MaxPowerKW > 0 && (
            <div>
              <dt className="ui-label">Max power</dt>
              <dd className="ui-mono text-neutral-100">{w.MaxPowerKW.toFixed(0)} kW</dd>
            </div>
          )}
          {guest != null && (
            <div>
              <dt className="ui-label">Cost</dt>
              <dd className="ui-mono text-neutral-100">
                {money(guest)}
                {member != null && <span className="ml-1 text-emerald-400" title={stopCost?.member_plan ?? "With your memberships"}>{money(member)}</span>}
              </dd>
            </div>
          )}
          {adapter && (
            <div className="col-span-2 text-amber-300">Needs a Tesla NACS adapter at this charger.</div>
          )}
        </dl>
      )}
    </li>
  );
}
