import { useQuery } from "@tanstack/react-query";
import { backend, type Vehicle, type VehicleState } from "../lib/api";
import { Card, ErrorBox, Spinner } from "./ui";
import { num, pct } from "../lib/format";
import { formatTemperature, usePreferences } from "../lib/preferences";

// Unit conversions. Backend speaks SI at the wire.
const kmToMi = (km: number) => km * 0.6213711922;
const kphToMph = (k: number) => k * 0.6213711922;
const barToPsi = (b: number) => b * 14.5037738;
const mToFt = (m: number) => m * 3.2808399;

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
    <div className="space-y-4">
      {list.map((v) => (
        <LiveVehicleCard key={v.id} vehicle={v} />
      ))}
    </div>
  );
}

function LiveVehicleCard({ vehicle }: { vehicle: Vehicle }) {
  const state = useQuery<VehicleState>({
    queryKey: ["rivian", "state", vehicle.id],
    queryFn: () => backend.vehicleState(vehicle.id),
    refetchInterval: 30_000,
    retry: 1,
  });
  const { temperatureUnit: tempUnit } = usePreferences();

  const name = vehicle.name || vehicle.model || vehicle.id;
  const s = state.data;

  return (
    <Card
      title={name}
      actions={
        s ? (
          <StatusPill state={s} />
        ) : state.isLoading ? (
          <Spinner />
        ) : null
      }
    >
      <div className="text-[11px] text-neutral-500">
        {vehicle.model}
        {vehicle.vin ? ` · VIN ${vehicle.vin.slice(-6)}` : ""}
      </div>
      {state.isError ? (
        <p className="mt-3 text-xs text-red-400">{String(state.error)}</p>
      ) : s ? (
        <div className="mt-4 space-y-4">
          <Section title="Energy">
            <Field label="Battery" value={pct(s.battery_level_pct, 0)} />
            <Field label="Range" value={num(kmToMi(s.distance_to_empty), 0, "mi")} />
            <Field label="Odometer" value={num(kmToMi(s.odometer_km), 0, "mi")} />
            <Field
              label="Charger"
              value={
                s.charger_power_kw > 0
                  ? num(s.charger_power_kw, 1, "kW")
                  : formatChargerState(s.charger_state)
              }
            />
            <Field label="Charge limit" value={pct(s.charge_target_pct, 0)} />
            <Field label="Plug" value={formatChargerStatus(s.charger_status)} />
            <Field label="Port" value={formatOpenClosed(s.charge_port_state)} />
            <Field
              label="Remote charging"
              value={formatBoolish(s.remote_charging_available)}
            />
          </Section>

          <Section title="Drive">
            <Field label="Gear" value={s.gear || "—"} />
            <Field label="Mode" value={formatDriveMode(s.drive_mode)} />
            <Field label="Power" value={formatPower(s.power_state)} />
            <Field label="Speed" value={num(kphToMph(s.speed_kph), 0, "mph")} />
            <Field label="Heading" value={formatHeading(s.heading_deg)} />
            <Field label="Altitude" value={num(mToFt(s.altitude_m), 0, "ft")} />
          </Section>

          <Section title="Climate">
            <Field label="Cabin" value={formatTemperature(s.cabin_temp_c, tempUnit)} />
            <Field label="Outside" value={formatTemperature(s.outside_temp_c, tempUnit)} />
            <Field
              label="Precondition"
              value={formatTitle(s.cabin_preconditioning_status)}
            />
          </Section>

          <Section title="Closures">
            <Field label="Locked" value={formatYesNo(s.locked)} />
            <Field label="Doors" value={formatClosed(s.doors_closed)} />
            <Field label="Frunk" value={formatClosed(s.frunk_closed)} />
            <Field label="Liftgate" value={formatClosed(s.liftgate_closed)} />
            {vehicle.model === "R1T" ? (
              <>
                <Field label="Tailgate" value={formatClosed(s.tailgate_closed)} />
                <Field label="Tonneau" value={formatClosed(s.tonneau_closed)} />
              </>
            ) : null}
          </Section>

          <Section title="Tires">
            <Field
              label="FL"
              value={formatTire(s.tire_pressure_fl_bar, s.tire_pressure_status_fl)}
            />
            <Field
              label="FR"
              value={formatTire(s.tire_pressure_fr_bar, s.tire_pressure_status_fr)}
            />
            <Field
              label="RL"
              value={formatTire(s.tire_pressure_rl_bar, s.tire_pressure_status_rl)}
            />
            <Field
              label="RR"
              value={formatTire(s.tire_pressure_rr_bar, s.tire_pressure_status_rr)}
            />
          </Section>

          <Section title="Software · Safety">
            <Field label="OTA installed" value={s.ota_current_version || "—"} />
            <Field
              label="OTA available"
              value={
                s.ota_available_version &&
                s.ota_available_version !== s.ota_current_version
                  ? s.ota_available_version
                  : "up-to-date"
              }
            />
            <Field label="OTA status" value={formatTitle(s.ota_status)} />
            {s.ota_install_progress > 0 ? (
              <Field label="Install" value={pct(s.ota_install_progress, 0)} />
            ) : null}
            <Field label="Alarm" value={formatBoolish(s.alarm_sound_status)} />
            <Field label="12V" value={formatTitle(s.twelve_volt_battery_health)} />
            <Field
              label="Wiper fluid"
              value={formatTitle(s.wiper_fluid_state)}
            />
          </Section>

          <div className="pt-1 text-[11px] text-neutral-500">
            Updated {new Date(s.at).toLocaleTimeString()}
          </div>
        </div>
      ) : null}
    </Card>
  );
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <div className="mb-1.5 text-[11px] uppercase tracking-wide text-neutral-500">
        {title}
      </div>
      <div className="grid grid-cols-2 gap-x-3 gap-y-2 sm:grid-cols-3 md:grid-cols-4">
        {children}
      </div>
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
    <span
      className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] ${tone}`}
    >
      {state.locked ? <LockIcon className="h-3 w-3" /> : null}
      {label}
    </span>
  );
}

function LockIcon({ className = "" }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
    >
      <rect x="4" y="11" width="16" height="10" rx="2" />
      <path d="M8 11V7a4 4 0 0 1 8 0v4" />
    </svg>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[11px] text-neutral-500">{label}</div>
      <div className="tabular-nums text-sm text-neutral-200">{value}</div>
    </div>
  );
}

// ---------- formatters ----------

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
    case "charging_ready":
      return "ready";
    default:
      return s || "—";
  }
}

function formatChargerStatus(s: string): string {
  switch (s) {
    case "chrgr_sts_not_connected":
      return "unplugged";
    case "chrgr_sts_connected_charging":
      return "charging";
    case "chrgr_sts_connected_no_power":
      return "plugged · no power";
    case "":
      return "—";
    default:
      return s.replace(/^chrgr_sts_/, "").replace(/_/g, " ");
  }
}

function formatOpenClosed(s: string): string {
  if (!s) return "—";
  return s.toLowerCase() === "open" ? "open" : "closed";
}

function formatClosed(closed: boolean): string {
  return closed ? "closed" : "open";
}

function formatYesNo(v: boolean): string {
  return v ? "yes" : "no";
}

function formatBoolish(s: string): string {
  if (!s) return "—";
  const v = s.toLowerCase();
  if (v === "true" || v === "on" || v === "active") return "yes";
  if (v === "false" || v === "off" || v === "inactive") return "no";
  return formatTitle(s);
}

function formatTitle(s: string): string {
  if (!s) return "—";
  return s.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}

function formatDriveMode(s: string): string {
  const map: Record<string, string> = {
    everyday: "All-Purpose",
    sport: "Sport",
    distance: "Conserve",
    winter: "Snow",
    towing: "Towing",
    off_road_auto: "All-Terrain",
    off_road_sand: "Soft Sand",
    off_road_rocks: "Rock Crawl",
    off_road_sport_auto: "Rally",
    off_road_sport_drift: "Drift",
  };
  return map[s] ?? formatTitle(s);
}

function formatPower(s: string): string {
  switch (s) {
    case "":
      return "—";
    case "go":
      return "Go";
    case "ready":
      return "Ready";
    case "sleep":
      return "Asleep";
    case "standby":
      return "Standby";
    case "vehicle_reset":
      return "Resetting";
    default:
      return formatTitle(s);
  }
}

function formatHeading(deg: number): string {
  if (!Number.isFinite(deg) || deg === 0) return "—";
  const dirs = ["N", "NE", "E", "SE", "S", "SW", "W", "NW"];
  const idx = Math.round(deg / 45) % 8;
  return `${dirs[idx]} · ${deg.toFixed(0)}°`;
}

function formatTire(bar: number, status: string): string {
  if (!bar) return formatTitle(status);
  const psi = barToPsi(bar);
  const st = status && status.toLowerCase() !== "normal" ? ` · ${status}` : "";
  return `${psi.toFixed(0)} psi${st}`;
}
