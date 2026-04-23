import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend, type Vehicle, type VehicleState } from "../lib/api";
import { Card } from "./ui";
import { num, pct } from "../lib/format";

const kmToMi = (km: number) => km * 0.6213711922;

// LiveSummary is the compact variant of <LivePanel/> meant to live on
// the Overview. Shows one line per vehicle with status + battery +
// range and links to the full /live page. Hidden when no vehicles are
// connected (same rationale as LivePanel).
export function LiveSummary() {
  const vehicles = useQuery({
    queryKey: ["rivian", "vehicles"],
    queryFn: () => backend.vehicles(),
    staleTime: 5 * 60_000,
  });

  if (vehicles.isLoading || vehicles.isError) return null;
  const list = vehicles.data ?? [];
  if (list.length === 0) return null;

  return (
    <Card
      title="Live"
      actions={
        <Link
          to="/live"
          className="text-xs text-emerald-400 hover:underline"
        >
          details →
        </Link>
      }
    >
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
    <li className="flex items-center justify-between gap-3 text-sm">
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
    </li>
  );
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
