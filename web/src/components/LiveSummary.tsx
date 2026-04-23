import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend, type Vehicle, type VehicleState } from "../lib/api";
import { Card } from "./ui";
import { num, pct } from "../lib/format";

const kmToMi = (km: number) => km * 0.6213711922;
const cToF = (c: number) => c * 1.8 + 32;

// LiveSummary is the compact variant of <LivePanel/> meant to live on
// the Overview. Always rendered so the user sees the Rivian connection
// state at a glance: hidden only when the live client is outright
// disabled (RIVIAN_CLIENT=stub). States:
//
//   - not authenticated → prompt to sign in (linked to Settings)
//   - authenticated, no vehicles → empty-state note
//   - vehicles present → one line per vehicle + link to /live
export function LiveSummary() {
  const status = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    staleTime: 30_000,
    retry: 1,
  });
  const vehicles = useQuery({
    queryKey: ["rivian", "vehicles"],
    queryFn: () => backend.vehicles(),
    staleTime: 5 * 60_000,
    // Only fetch vehicles when we know we're authenticated — otherwise
    // the 502 from /api/vehicles just pollutes devtools.
    enabled: !!status.data?.authenticated,
    retry: 1,
  });

  // Still loading the status for the first time: don't flash an empty
  // card, the rest of the page will draw in a moment.
  if (status.isLoading) return null;

  // Stub mode — no account UI to offer, so stay out of the way.
  if (status.isError || !status.data?.enabled) return null;

  const header = (
    <Link to="/live" className="text-xs text-emerald-400 hover:underline">
      details →
    </Link>
  );

  if (!status.data.authenticated) {
    return (
      <Card title="Live" actions={header}>
        <p className="text-sm text-neutral-400">
          Not connected to Rivian.{" "}
          <Link to="/settings" className="text-emerald-400 hover:underline">
            Sign in →
          </Link>
        </p>
      </Card>
    );
  }

  const list = vehicles.data ?? [];
  if (vehicles.isLoading) {
    return (
      <Card title="Live" actions={header}>
        <p className="text-sm text-neutral-500">loading…</p>
      </Card>
    );
  }
  if (vehicles.isError) {
    return (
      <Card title="Live" actions={header}>
        <p className="text-sm text-rose-400">
          Rivian API error. Try{" "}
          <Link to="/settings" className="text-emerald-400 hover:underline">
            re-signing in
          </Link>
          .
        </p>
      </Card>
    );
  }
  if (list.length === 0) {
    return (
      <Card title="Live" actions={header}>
        <p className="text-sm text-neutral-400">
          Signed in as{" "}
          <span className="text-neutral-200">{status.data.email || "—"}</span>,
          but no vehicles on this account.
        </p>
      </Card>
    );
  }

  return (
    <Card title="Live" actions={header}>
      <ul className="space-y-1.5">
        {list.map((v) => (
          <LiveSummaryRow key={v.id} vehicle={v} />
        ))}
      </ul>
    </Card>
  );
}

function LiveSummaryRow({ vehicle }: { vehicle: Vehicle }) {
  const state = useQuery<VehicleState>({
    queryKey: ["rivian", "state", vehicle.id],
    queryFn: () => backend.vehicleState(vehicle.id),
    refetchInterval: 60_000,
    retry: 1,
  });

  const name = vehicle.name || vehicle.model || vehicle.id;
  const s = state.data;

  return (
    <li className="space-y-2">
      <div className="flex items-center justify-between gap-3 text-sm">
        <Link
          to="/live"
          className="truncate font-medium text-neutral-200 hover:text-emerald-300"
        >
          {name}
        </Link>
        <div className="flex items-center gap-3 tabular-nums text-neutral-400">
          {s ? (
            <>
              <span>{pct(s.battery_level_pct, 0)}</span>
              <span className="text-neutral-600">·</span>
              <span>{num(kmToMi(s.distance_to_empty), 0, "mi")}</span>
              <StatusDot state={s} />
            </>
          ) : state.isError ? (
            <span className="text-rose-400 text-xs">error</span>
          ) : (
            <span className="text-neutral-500 text-xs">loading…</span>
          )}
        </div>
      </div>
      {s ? (
        <div className="grid grid-cols-2 gap-x-3 gap-y-1 sm:grid-cols-4 md:grid-cols-6">
          <Stat label="Odo" value={num(kmToMi(s.odometer_km), 0, "mi")} />
          <Stat label="Limit" value={pct(s.charge_target_pct, 0)} />
          <Stat label="Cabin" value={num(cToF(s.cabin_temp_c), 0, "°F")} />
          <Stat label="Gear" value={s.gear || "—"} />
          <Stat label="Power" value={formatPowerShort(s.power_state)} />
          <Stat
            label="Plug"
            value={
              s.charger_power_kw > 0
                ? num(s.charger_power_kw, 1, "kW")
                : formatPlugShort(s.charger_status)
            }
          />
        </div>
      ) : null}
    </li>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wide text-neutral-500">
        {label}
      </div>
      <div className="tabular-nums text-sm text-neutral-300">{value}</div>
    </div>
  );
}

function formatPowerShort(s: string): string {
  switch (s) {
    case "sleep":
      return "asleep";
    case "go":
      return "go";
    case "ready":
      return "ready";
    case "standby":
      return "standby";
    case "":
      return "—";
    default:
      return s.replace(/_/g, " ");
  }
}

function formatPlugShort(s: string): string {
  switch (s) {
    case "chrgr_sts_not_connected":
      return "unplugged";
    case "chrgr_sts_connected_charging":
      return "charging";
    case "chrgr_sts_connected_no_power":
      return "plugged";
    case "":
      return "—";
    default:
      return s.replace(/^chrgr_sts_/, "").replace(/_/g, " ");
  }
}

function StatusDot({ state }: { state: VehicleState }) {
  const charging = state.charger_power_kw > 0;
  const moving = state.gear && state.gear !== "P";
  const label = charging ? "charging" : moving ? "driving" : "parked";
  const tone = charging
    ? "bg-emerald-400"
    : moving
      ? "bg-blue-400"
      : "bg-neutral-500";
  return (
    <span className="flex items-center gap-1 text-[11px] text-neutral-400">
      <span className={`h-1.5 w-1.5 rounded-full ${tone}`} />
      {label}
    </span>
  );
}
