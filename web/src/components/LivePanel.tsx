import { useQuery } from "@tanstack/react-query";
import { backend, type Vehicle, type VehicleState } from "../lib/api";
import { Card, ErrorBox, Spinner } from "./ui";
import { num, pct } from "../lib/format";

// Converts km → miles for display. The backend speaks SI at the wire.
const kmToMi = (km: number) => km * 0.6213711922;
const cToF = (c: number) => c * 1.8 + 32;

// LivePanel polls the connected Rivian client (live or mock) and
// renders one card per vehicle with the current state. Hidden entirely
// when /api/vehicles returns an empty list — Rivolt still works fine
// as a read-only ElectraFi dashboard without a connected Rivian
// account, and we don't want an empty "Live" section to suggest it's
// broken.
export function LivePanel() {
  const vehicles = useQuery({
    queryKey: ["rivian", "vehicles"],
    queryFn: () => backend.vehicles(),
    // Vehicles change rarely; re-check when the tab regains focus.
    staleTime: 5 * 60_000,
  });

  if (vehicles.isLoading) return null;
  if (vehicles.isError) {
    return (
      <ErrorBox
        title="Rivian connection error"
        detail={String(vehicles.error)}
      />
    );
  }
  const list = vehicles.data ?? [];
  if (list.length === 0) return null;
  return (
    <Card title="Live">
      <div className="grid gap-3 md:grid-cols-2">
        {list.map((v) => (
          <LiveVehicleCard key={v.id} vehicle={v} />
        ))}
      </div>
    </Card>
  );
}

function LiveVehicleCard({ vehicle }: { vehicle: Vehicle }) {
  const state = useQuery<VehicleState>({
    queryKey: ["rivian", "state", vehicle.id],
    queryFn: () => backend.vehicleState(vehicle.id),
    // Live polling. 30 s is what the Rivian app uses and avoids
    // burning through the (undocumented) rate limit.
    refetchInterval: 30_000,
    retry: 1,
  });

  const name = vehicle.name || vehicle.model || vehicle.id;

  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-3">
      <div className="flex items-center justify-between">
        <div>
          <div className="text-sm font-medium text-neutral-200">{name}</div>
          <div className="text-[11px] text-neutral-500">
            {vehicle.vin ? `VIN ${vehicle.vin.slice(-6)}` : vehicle.id}
          </div>
        </div>
        {state.data ? (
          <StatusPill state={state.data} />
        ) : state.isLoading ? (
          <Spinner />
        ) : null}
      </div>
      {state.isError ? (
        <p className="mt-2 text-xs text-red-400">{String(state.error)}</p>
      ) : state.data ? (
        <div className="mt-3 grid grid-cols-2 gap-2 text-sm">
          <Field label="Battery" value={pct(state.data.battery_level_pct, 0)} />
          <Field
            label="Range"
            value={num(kmToMi(state.data.distance_to_empty), 0, "mi")}
          />
          <Field
            label="Odometer"
            value={num(kmToMi(state.data.odometer_km), 0, "mi")}
          />
          <Field label="Gear" value={state.data.gear || "—"} />
          <Field
            label="Charger"
            value={
              state.data.charger_power_kw > 0
                ? num(state.data.charger_power_kw, 1, "kW")
                : formatChargerState(state.data.charger_state)
            }
          />
          <Field
            label="Cabin · Outside"
            value={`${num(cToF(state.data.cabin_temp_c), 0, "°F")} · ${num(
              cToF(state.data.outside_temp_c),
              0,
              "°F",
            )}`}
          />
        </div>
      ) : null}
    </div>
  );
}

function StatusPill({ state }: { state: VehicleState }) {
  const charging = state.charger_power_kw > 0;
  const moving = state.gear && state.gear !== "P";
  const label = charging ? "charging" : moving ? "driving" : "parked";
  const tone = charging
    ? "bg-emerald-500/15 text-emerald-300"
    : moving
      ? "bg-blue-500/15 text-blue-300"
      : "bg-neutral-700/40 text-neutral-300";
  return (
    <span className={`rounded-full px-2 py-0.5 text-[11px] ${tone}`}>
      {state.locked ? "🔒 " : ""}
      {label}
    </span>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[11px] text-neutral-500">{label}</div>
      <div className="tabular-nums text-neutral-200">{value}</div>
    </div>
  );
}

// Rivian's chargerState values are snake_case identifiers. Humanise
// the handful we care about here; unknowns pass through.
function formatChargerState(s: string): string {
  switch (s) {
    case "charger_disconnected":
      return "disconnected";
    case "charger_connected":
      return "connected";
    case "charging_active":
      return "active";
    case "charging_complete":
      return "complete";
    default:
      return s || "—";
  }
}
