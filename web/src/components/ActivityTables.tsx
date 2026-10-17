import { formatMoney } from "../lib/ledger";
import { appPath } from "../lib/demo";
import { useNavigate } from "react-router-dom";
import { clickableRowProps } from "./ui";
import type { Charge, ChargeCluster, Drive } from "../lib/api";
import { chargePlaceLabels, type ChargePlaceLabel } from "../lib/chargePlace";
import { driveDurationSeconds } from "../lib/drives";
import {
  durationSeconds,
  formatChargeState,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";

// The drive and charge tables shared by the Drives and Charges pages and the
// Activity page's Table view.

export function DriveTable({ drives, compact = false }: { drives: Drive[]; compact?: boolean }) {
  const navigate = useNavigate();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm" data-activity-table={compact ? "" : undefined}>
        <thead>
          <tr className="text-left text-xs uppercase tracking-wide text-neutral-500">
            <th data-col="start" className="py-2 pr-4 font-medium">Start</th>
            <th data-col="duration" className="py-2 pr-4 font-medium">Duration</th>
            <th data-col="distance" className="py-2 pr-4 font-medium">Distance</th>
            <th data-col="soc" className="py-2 pr-4 font-medium">SoC</th>
            <th data-col="speed" className="py-2 pr-4 font-medium">Avg / Max</th>
            <th data-col="energy" className="py-2 pr-4 font-medium">Energy</th>
            <th data-col="cost" className="py-2 pr-4 font-medium">Cost</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-neutral-800">
          {drives.map((d) => (
            <tr
              key={d.ID}
              className="cursor-pointer hover:bg-neutral-900/60 focus:outline-hidden focus-visible:bg-neutral-800/80"
              {...clickableRowProps(() => navigate(appPath(`/drives/${d.ID}`)), {
                role: "link",
                label: `Open drive from ${formatDateTime(d.StartedAt)}`,
              })}
            >
              <td data-col="start" className="py-2 pr-4 text-neutral-300 whitespace-nowrap">
                {formatDateTime(d.StartedAt)}
              </td>
              <td data-col="duration" className="py-2 pr-4 text-neutral-400 tabular-nums">
                {formatDuration(driveDurationSeconds(d))}
              </td>
              <td data-col="distance" className="py-2 pr-4 text-neutral-200 tabular-nums">
                {num(d.DistanceMi, 1, "mi")}
              </td>
              <td data-col="soc" className="py-2 pr-4 text-neutral-400 tabular-nums">
                {pct(d.StartSoCPct)} → {pct(d.EndSoCPct)}
              </td>
              <td data-col="speed" className="py-2 pr-4 text-neutral-400 tabular-nums">
                {num(d.AvgSpeedMph, 0)} / {num(d.MaxSpeedMph, 0)} mph
              </td>
              <td data-col="energy" className="py-2 pr-4 text-neutral-400 tabular-nums">
                {d.EnergyUsedKWh > 0 ? num(d.EnergyUsedKWh, 1, "kWh") : "—"}
              </td>
              <td data-col="cost"
                className="py-2 pr-4 text-neutral-400 tabular-nums"
                title={
                  d.estimated_price_per_kwh
                    ? `Estimated at ${d.estimated_price_per_kwh.toFixed(3)} ${d.estimated_currency ?? ""}/kWh — rate from the most recent charge before this drive`
                    : undefined
                }
              >
                {d.estimated_cost && d.estimated_cost > 0
                  ? `~${formatMoney(d.estimated_cost, d.estimated_currency || "USD")}`
                  : "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// chargingSeconds returns the time a session actually spent charging.
// The recorder persists that as ActiveSeconds (charger power above the
// idle floor); it excludes the post-target idle that inflates the raw
// EndedAt-StartedAt span. Legacy rows, imports, and non-Parallax live
// sessions have no ActiveSeconds, so fall back to the wall span there.
export function chargingSeconds(c: Charge): number {
  return c.ActiveSeconds && c.ActiveSeconds > 0
    ? c.ActiveSeconds
    : durationSeconds(c.StartedAt, c.EndedAt);
}

export function ChargeTable({
  charges,
  labelByID,
  compact = false,
}: {
  charges: Charge[];
  labelByID: Map<string, ChargePlaceLabel>;
  // The Activity Table view (ui_theme_next): the secondary columns drop out on a phone.
  compact?: boolean;
}) {
  const navigate = useNavigate();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm" data-activity-table={compact ? "" : undefined}>
        <thead>
          <tr className="text-left text-xs uppercase tracking-wide text-neutral-500">
            <th data-col="start" className="py-2 pr-4 font-medium">Start</th>
            <th data-col="location" className="py-2 pr-4 font-medium">Location</th>
            <th data-col="duration" className="py-2 pr-4 font-medium">Duration</th>
            <th data-col="soc" className="py-2 pr-4 font-medium">SoC</th>
            <th data-col="energy" className="py-2 pr-4 font-medium">Energy</th>
            <th data-col="maxkw" className="py-2 pr-4 font-medium">Max kW</th>
            <th data-col="cost" className="py-2 pr-4 font-medium">Cost</th>
            <th data-col="state" className="py-2 pr-4 font-medium">Final state</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-neutral-800">
          {charges.map((c) => (
            <tr
              key={c.ID}
              className="cursor-pointer hover:bg-neutral-900/60 focus:outline-hidden focus-visible:bg-neutral-800/80"
              {...clickableRowProps(() => navigate(appPath(`/charges/${c.ID}`)), {
                role: "link",
                label: `Open charge from ${formatDateTime(c.StartedAt)}`,
              })}
            >
              <td data-col="start" className="py-2 pr-4 text-neutral-300 whitespace-nowrap">
                {formatDateTime(c.StartedAt)}
              </td>
              <td data-col="location" className="py-2 pr-4 whitespace-nowrap">
                <LocationBadge label={labelByID.get(c.ID) ?? ""} />
              </td>
              <td data-col="duration" className="py-2 pr-4 text-neutral-400 tabular-nums">
                {formatDuration(chargingSeconds(c))}
              </td>
              <td data-col="soc" className="py-2 pr-4 text-neutral-400 tabular-nums">
                {pct(c.StartSoCPct)} → {pct(c.EndSoCPct)}
              </td>
              <td data-col="energy" className="py-2 pr-4 text-neutral-200 tabular-nums">
                {num(c.EnergyAddedKWh, 1, "kWh")}
              </td>
              <td data-col="maxkw" className="py-2 pr-4 text-neutral-400 tabular-nums">
                {num(c.MaxPowerKW, 1)}
              </td>
              <td data-col="cost" className="py-2 pr-4 text-neutral-400 tabular-nums">
                {c.Cost > 0
                  ? formatMoney(c.Cost, c.Currency || "USD")
                  : c.estimated_cost && c.estimated_cost > 0
                    ? `~${formatMoney(c.estimated_cost, c.estimated_currency || "USD")}`
                    : "—"}
              </td>
              <td data-col="state" className="py-2 pr-4 text-neutral-500">{formatChargeState(c.FinalState)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// labelMap is the per-charge Home / Away / Fast label the table colours
// rows by: the server's classification (lib/chargePlace), the cluster
// label only where the server couldn't say. No label renders a dash.
export function labelMap(charges: Charge[], clusters: ChargeCluster[]): Map<string, ChargePlaceLabel> {
  return chargePlaceLabels(charges, clusters);
}

// LocationBadge renders the Home/Away/Fast tag. Styling follows the
// convention of the rest of the app: Home leans emerald (positive /
// matches the "ready" pill on Settings), Fast is amber (attention —
// DCFC stops are the expensive ones), Away is plain neutral because
// it's the catch-all for AC charging away from home.
function LocationBadge({ label }: { label: ChargePlaceLabel }) {
  if (!label) {
    return <span className="text-neutral-600">—</span>;
  }
  const tone =
    label === "Home"
      ? "border-emerald-600/40 text-emerald-300 bg-emerald-950/30"
      : label === "Fast"
        ? "border-amber-600/40 text-amber-300 bg-amber-950/30"
        : "border-neutral-700 text-neutral-400";
  return (
    <span
      className={`text-xs px-2 py-0.5 rounded-full border ${tone}`}
      title={
        label === "Fast"
          ? "DC fast charge"
          : "By distance from your home location"
      }
    >
      {label}
    </span>
  );
}
