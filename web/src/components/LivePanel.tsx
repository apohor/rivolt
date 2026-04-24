import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend, type Charge, type Drive, type LiveDrive, type LiveSession, type Vehicle, type VehicleState } from "../lib/api";
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

  // Pick the configurator image that matches the current vehicle
  // state — charging shows side-charging, driving shows in-use,
  // frunk open shows front, liftgate/tonneau shows rear, etc.
  const heroUrl = pickImageForState(vehicle.images, s) || vehicle.image_url || "";

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
        {vehicle.trim_name ? ` · ${vehicle.trim_name}` : ""}
        {vehicle.model_year ? ` · ${vehicle.model_year}` : ""}
        {vehicle.pack_kwh ? ` · ${vehicle.pack_kwh} kWh pack` : ""}
        {vehicle.vin ? ` · VIN ${vehicle.vin.slice(-6)}` : ""}
      </div>
      {/* Image + current-activity panel share a row on md+. Image
          takes 1/3 (it's just a recognizable silhouette — no data
          to scan), session panel takes 2/3 so cost/power/speed/etc.
          get the space they need. Stacks on mobile. */}
      <div className="mt-3 grid gap-4 md:grid-cols-3">
        {heroUrl ? (
          <div className="flex items-center justify-center md:col-span-1">
            {/* Configurator PNGs have huge horizontal margins baked
                in; max-h-40 / max-w-sm keeps the car recognizable
                without the left column dwarfing the status panel. */}
            <img
              src={heroUrl}
              alt={name}
              loading="lazy"
              className="max-h-40 w-full max-w-sm rounded-md object-contain"
            />
          </div>
        ) : (
          <div className="md:col-span-1" />
        )}
        <div className="flex flex-col justify-center md:col-span-2">
          {state.isError ? (
            <p className="text-xs text-red-400">{String(state.error)}</p>
          ) : s ? (
            isCharging(s) ? (
              <ChargingDetail vehicle={vehicle} state={s} />
            ) : isDriving(s) ? (
              <DrivingDetail vehicle={vehicle} state={s} />
            ) : (
              <ParkedSummary vehicle={vehicle} state={s} />
            )
          ) : (
            <div className="text-[11px] text-neutral-500">loading…</div>
          )}
        </div>
      </div>
      {state.isError ? null : s ? (
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
            <Field label="Gear" value={formatGear(s.gear)} />
            <Field label="Mode" value={formatDriveMode(s.drive_mode)} />
            <Field label="Power" value={formatPower(s.power_state)} />
            <Field label="Speed" value={num(kphToMph(s.speed_kph), 0, "mph")} />
            <Field label="Heading" value={formatHeading(s.heading_deg)} />
            <Field label="Altitude" value={num(mToFt(s.altitude_m), 0, "ft")} />
          </Section>

          <Section title="Climate">
            <Field label="Cabin" value={formatTemperature(s.cabin_temp_c, tempUnit)} />
            {/* Rivian's live VehicleState GraphQL feed doesn't expose
                an outside/ambient temperature field — OutsideTempC
                is hardcoded to 0 in internal/rivian/live.go. Hide
                the Field entirely rather than render a misleading
                "0 °C". Drive samples persisted from electrafi CSVs
                do carry outside temp and surface on /drives/:id. */}
            {s.outside_temp_c !== 0 ? (
              <Field label="Outside" value={formatTemperature(s.outside_temp_c, tempUnit)} />
            ) : null}
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
              value={formatOtaAvailable(s.ota_available_version, s.ota_current_version)}
            />
            <Field label="OTA status" value={formatTitle(s.ota_status)} />
            {s.ota_install_progress > 0 ? (
              <Field label="Install" value={pct(s.ota_install_progress, 0)} />
            ) : null}
            <Field label="Alarm" value={formatBoolish(s.alarm_sound_status)} />
            <Field label="12V" value={formatTwelveVolt(s.twelve_volt_battery_health)} />
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

// isCharging is true only when the car is actually plugged in AND a
// session is active (or transitioning to active). Rivian's
// charger_state sticks at 'charging_ready' / 'charging_active' /
// 'charging_complete' across unplugs — without the charger_status
// plug check the live panel keeps rendering a "Charging session"
// long after the cable is out.
//
// A completed session (charger_state=='charging_complete') is
// explicitly NOT charging even if the cable is still in the port —
// Rivian's live-session feed returns a zeroed payload that would
// otherwise render as "complete · waiting for first frame…", which
// is misleading. When the session is done we fall through to
// ParkedSummary, which surfaces the idle / asleep state and a
// "plugged in" label so the cable status is still visible.
function isCharging(s: VehicleState): boolean {
  if (!isPluggedIn(s)) return false;
  const cs = s.charger_state || "";
  if (cs === "charging_complete") return false;
  if (s.charger_power_kw > 0) return true;
  return cs === "charging_active" || cs === "charging_ready";
}

// isPluggedIn interprets Rivian's charger_status field. The
// connected-charging and connected-not-charging states are the only
// ones that mean there's a physical cable in the port.
function isPluggedIn(s: VehicleState): boolean {
  const st = (s.charger_status || "").toLowerCase();
  return st.startsWith("chrgr_sts_connected");
}

// ChargingDetail renders Rivian's live charging session data —
// power, rate, range added, energy delivered, ETA, price. Polls
// /api/live-session every 10 s while visible. Hidden when the
// session is inactive or hasn't started reporting yet.
function ChargingDetail({
  vehicle,
  state,
}: {
  vehicle: Vehicle;
  state: VehicleState;
}) {
  const sess = useQuery<LiveSession>({
    queryKey: ["rivian", "live-session", vehicle.id],
    queryFn: () => backend.liveSession(vehicle.id),
    refetchInterval: 10_000,
    retry: 1,
  });
  const ls = sess.data;
  // Prefer live-session power when available, fall back to state.
  const powerKw = ls && ls.power_kw > 0 ? ls.power_kw : state.charger_power_kw;
  const ratePerHour =
    ls && ls.kilometers_charged_per_hour > 0
      ? kmToMi(ls.kilometers_charged_per_hour)
      : 0;
  const rangeAdded = ls ? kmToMi(ls.range_added_km) : 0;
  const energyKwh = ls ? ls.total_charged_energy_kwh : 0;
  const elapsed = ls ? ls.time_elapsed_seconds : 0;
  const remaining = ls ? ls.time_remaining_seconds : 0;
  const price = ls && ls.current_price ? ls.current_price : "";
  const currency = ls ? ls.current_currency : "";
  const targetPct = state.charge_target_pct;
  const soc = ls && ls.soc_pct > 0 ? ls.soc_pct : state.battery_level_pct;
  const toTarget = Math.max(0, targetPct - soc);

  // Session kind derived from state first, then live-session. Home AC
  // sessions come back with active=false + a zeroed payload from
  // Rivian's chrg/user endpoint, so we can't rely on ls.active as
  // "is there a session" — the plug/charger_state on the vehicle is
  // authoritative.
  const cs = state.charger_state || "";
  const isActiveState = cs === "charging_active" || state.charger_power_kw > 0;
  const kind = ls?.is_rivian_charger
    ? "Rivian charger"
    : isActiveState
      ? "Home / AC"
      : cs === "charging_ready"
        ? "ready · plugged in"
        : cs === "charging_complete"
          ? "complete"
          : "idle";

  // True when Rivian's live feed gave us nothing useful — typical for
  // home AC / L1 / L2 sessions. We still render the panel (user IS
  // charging per vehicleState) but swap in an explanation block
  // instead of a grid full of em-dashes.
  const haveLiveData = !!ls && (ls.active || ls.power_kw > 0 || ls.total_charged_energy_kwh > 0);

  return (
    <div className="rounded-lg border border-emerald-500/20 bg-emerald-500/5 p-3">
      <div className="mb-2 flex items-center justify-between">
        <div className="text-[11px] uppercase tracking-wide text-emerald-300">
          Charging session
        </div>
        <div className="text-[11px] text-neutral-500">
          {sess.isLoading ? "…" : kind}
        </div>
      </div>
      {/* Top row: big readout of the current power + SoC progress */}
      <div className="mb-3 flex items-baseline gap-4">
        <div className="tabular-nums text-3xl font-semibold text-emerald-200">
          {powerKw > 0 ? powerKw.toFixed(1) : "0.0"}
          <span className="ml-1 text-sm font-normal text-emerald-400/70">kW</span>
        </div>
        <div className="flex-1">
          <div className="mb-0.5 flex justify-between text-[11px] text-neutral-400">
            <span>{pct(soc, 0)}</span>
            <span>→ {pct(targetPct, 0)}</span>
          </div>
          <div className="relative h-1.5 w-full overflow-hidden rounded-full bg-neutral-800">
            <div
              className="h-full bg-emerald-400"
              style={{ width: `${Math.min(100, Math.max(0, soc))}%` }}
            />
            {/* Target SoC marker — only rendered when a charge target
                is reported and we're not already past it. Positioned
                absolutely so it can't shift the bar's layout. */}
            {targetPct > 0 && targetPct < 100 && soc < targetPct ? (
              <div
                className="absolute top-[-2px] h-[10px] w-px bg-emerald-200/80"
                style={{ left: `${targetPct}%` }}
                aria-hidden="true"
                title={`Target ${pct(targetPct, 0)}`}
              />
            ) : null}
          </div>
          {toTarget > 0 ? (
            <div className="mt-0.5 text-[11px] text-neutral-500">
              {pct(toTarget, 0)} to target
            </div>
          ) : null}
        </div>
      </div>
      {haveLiveData ? (
        <div className="grid grid-cols-2 gap-x-3 gap-y-2 sm:grid-cols-3 md:grid-cols-4">
          <Field
            label="Rate"
            value={ratePerHour > 0 ? num(ratePerHour, 0, "mi/h") : "—"}
          />
          <Field
            label="Range added"
            value={rangeAdded > 0 ? num(rangeAdded, 0, "mi") : "—"}
          />
          <Field
            label="Energy"
            value={energyKwh > 0 ? num(energyKwh, 2, "kWh") : "—"}
          />
          <Field
            label="Elapsed"
            value={elapsed > 0 ? formatDuration(elapsed) : "—"}
          />
          <Field
            label="Remaining"
            value={remaining > 0 ? formatDuration(remaining) : "—"}
          />
          <Field label="State" value={formatChargerState(state.charger_state)} />
          <Field
            label="Price"
            value={
              price
                ? formatPrice(price, currency)
                : ls && ls.estimated_cost && ls.estimated_cost > 0
                  ? `~${formatPrice(ls.estimated_cost.toFixed(2), ls.estimated_currency || "")}`
                  : ls?.is_free_session
                    ? "free"
                    : "—"
            }
          />
        </div>
      ) : (
        // Compact fallback when the first telemetry frame hasn't
        // arrived yet. The long explainer we used to show here is
        // gone in favor of a single-line state readout — once
        // Parallax is connected the first frame lands in a few
        // seconds, so verbose copy adds noise more often than help.
        <div className="text-[11px] text-neutral-400">
          <span className="font-medium text-neutral-300">
            {formatChargerState(state.charger_state)}
          </span>
          {" · "}waiting for first frame…
        </div>
      )}
      {sess.isError ? (
        <p className="mt-2 text-[11px] text-red-400/70">
          Live-session fetch failed: {String(sess.error)}
        </p>
      ) : null}
    </div>
  );
}

// isDriving is true when the car is in a forward / reverse / neutral
// gear. The recorder uses the same predicate to open a drive
// accumulator, so the panel's visibility stays in sync with whether
// /api/live-drive has anything to return.
function isDriving(s: VehicleState): boolean {
  const g = (s.gear || "").toUpperCase();
  return g === "D" || g === "R" || g === "N";
}

// ParkedSummary is the "nothing is happening right now" placeholder
// that occupies the session slot when the car is neither charging
// nor driving. Pulls the most recent drive + charge so the user
// sees context at a glance instead of an empty half of the card.
function ParkedSummary({
  vehicle,
  state,
}: {
  vehicle: Vehicle;
  state: VehicleState;
}) {
  // Pull the full lists — they're already cached by /drives and
  // /charges pages (same queryKey), so this is usually a free hit.
  const drives = useQuery<Drive[]>({
    queryKey: ["drives", "all"],
    queryFn: () => backend.allDrives(),
    staleTime: 60_000,
  });
  const charges = useQuery<Charge[]>({
    queryKey: ["charges", "all"],
    queryFn: () => backend.allCharges(),
    staleTime: 60_000,
  });
  const lastDrive = drives.data?.[0];
  const lastCharge = charges.data?.[0];
  const plugged = isPlugged(state);

  return (
    <div className="rounded-lg border border-neutral-800 bg-neutral-900/40 p-3">
      <div className="mb-2 flex items-center justify-between">
        <div className="text-[11px] uppercase tracking-wide text-neutral-400">
          {plugged ? "Parked · plugged in" : "Parked"}
        </div>
        <div className="text-[11px] text-neutral-500">
          {/*
            charger_state sticks at "charging_ready" for hours after
            the cable is unplugged (same stale-field problem isPlugged
            was introduced to dodge). When the plug is physically out,
            the session label is meaningless — show power_state
            instead so a sleeping car reads "asleep" rather than a
            phantom "ready".
          */}
          {plugged
            ? formatChargerState(state.charger_state) || "idle"
            : formatPower(state.power_state).toLowerCase()}
        </div>
      </div>
      <div className="mb-3 flex items-baseline gap-4">
        <div className="tabular-nums text-2xl font-semibold text-neutral-200">
          {pct(state.battery_level_pct, 0)}
        </div>
        <div className="flex-1 text-[11px] text-neutral-400">
          <div className="flex justify-between">
            <span>{num(kmToMi(state.distance_to_empty), 0, "mi range")}</span>
            <span>limit {pct(state.charge_target_pct, 0)}</span>
          </div>
        </div>
      </div>
      <div className="grid grid-cols-2 gap-x-3 gap-y-2 text-[11px]">
        <div>
          <div className="mb-0.5 uppercase tracking-wide text-neutral-500">
            Last drive
          </div>
          {lastDrive ? (
            <Link
              to={`/drives/${encodeURIComponent(lastDrive.ID)}`}
              className="block hover:text-emerald-300"
            >
              <div className="tabular-nums text-neutral-200">
                {num(lastDrive.DistanceMi, 1, "mi")}
              </div>
              <div className="text-neutral-500">
                {timeAgo(lastDrive.EndedAt)}
              </div>
            </Link>
          ) : (
            <div className="text-neutral-500">—</div>
          )}
        </div>
        <div>
          <div className="mb-0.5 uppercase tracking-wide text-neutral-500">
            Last charge
          </div>
          {lastCharge ? (
            <Link
              to={`/charges/${encodeURIComponent(lastCharge.ID)}`}
              className="block hover:text-emerald-300"
            >
              <div className="tabular-nums text-neutral-200">
                {num(lastCharge.EnergyAddedKWh, 1, "kWh")}
              </div>
              <div className="text-neutral-500">
                {pct(lastCharge.StartSoCPct, 0)}→{pct(lastCharge.EndSoCPct, 0)} ·{" "}
                {timeAgo(lastCharge.EndedAt)}
              </div>
            </Link>
          ) : (
            <div className="text-neutral-500">—</div>
          )}
        </div>
      </div>
      {/* Reference the vehicle so the param isn't unused — kept for
          future plumbing (e.g. pack-size-aware efficiency stats). */}
      <span className="hidden" data-vehicle={vehicle.id} />
    </div>
  );
}

// isPlugged reports whether the charge cable is physically connected
// but not actively drawing power (so we're parked, not charging).
// Uses charger_status (real-time plug indicator) not charger_state,
// which sticks at 'charging_ready'/'charging_complete' for hours
// after the cable is pulled — see v0.3.48 notes in isPluggedIn().
function isPlugged(s: VehicleState): boolean {
  const st = (s.charger_status || "").toLowerCase();
  return st.startsWith("chrgr_sts_connected");
}

// timeAgo is a tiny relative-time formatter. Full localized dates
// live in /drives and /charges; here we just want a glance at how
// stale the last activity is.
function timeAgo(iso: string): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t) || t === 0) return "—";
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h}h ago`;
  const d = Math.round(h / 24);
  return `${d}d ago`;
}

// DrivingDetail renders the in-flight drive snapshot — elapsed time,
// distance, battery used, avg/max speed, efficiency. Polls
// /api/live-drive every 5 s while visible. Hidden when the endpoint
// replies 204 (no open drive on the backend yet — first telemetry
// frame after gear transition hasn't landed).
function DrivingDetail({
  vehicle,
  state,
}: {
  vehicle: Vehicle;
  state: VehicleState;
}) {
  const q = useQuery<LiveDrive | undefined>({
    queryKey: ["rivian", "live-drive", vehicle.id],
    queryFn: () => backend.liveDrive(vehicle.id),
    refetchInterval: 5_000,
    retry: 1,
  });
  const d = q.data;
  // Current speed comes straight from vehicleState — the session
  // snapshot only carries aggregates (avg/max). Showing "now" next to
  // "avg / max" gives an at-a-glance sense of pace.
  const nowMph = kphToMph(state.speed_kph);
  if (!d) {
    return (
      <div className="rounded-lg border border-blue-500/20 bg-blue-500/5 p-3 text-[11px] text-neutral-400">
        <span className="font-medium text-blue-300">
          {state.gear || "driving"}
        </span>
        {" · "}waiting for first frame…
      </div>
    );
  }
  return (
    <div className="rounded-lg border border-blue-500/20 bg-blue-500/5 p-3">
      <div className="mb-2 flex items-center justify-between">
        <div className="text-[11px] uppercase tracking-wide text-blue-300">
          Drive in progress
        </div>
        <div className="text-[11px] text-neutral-500">
          #{d.number} · {state.gear || "—"}
        </div>
      </div>
      <div className="mb-3 flex items-baseline gap-4">
        <div className="tabular-nums text-2xl font-semibold text-blue-200">
          {nowMph > 0 ? nowMph.toFixed(0) : "0"}
          <span className="ml-1 text-sm font-normal text-blue-400/70">mph</span>
        </div>
        <div className="flex-1 text-[11px] text-neutral-400">
          <div className="flex justify-between">
            <span>{num(d.distance_mi, 1, "mi")}</span>
            <span>{formatDuration(d.elapsed_sec)}</span>
          </div>
        </div>
      </div>
      <div className="grid grid-cols-2 gap-x-3 gap-y-2 sm:grid-cols-3 md:grid-cols-4">
        <Field label="Distance" value={num(d.distance_mi, 1, "mi")} />
        <Field label="Elapsed" value={formatDuration(d.elapsed_sec)} />
        <Field
          label="Battery used"
          value={d.soc_used_pct > 0 ? pct(d.soc_used_pct, 1) : "—"}
        />
        <Field
          label="Energy used"
          value={d.energy_used_kwh > 0 ? num(d.energy_used_kwh, 2, "kWh") : "—"}
        />
        <Field
          label="Efficiency"
          value={d.mi_per_kwh > 0 ? `${d.mi_per_kwh.toFixed(2)} mi/kWh` : "—"}
        />
        <Field
          label="Avg speed"
          value={d.avg_speed_mph > 0 ? num(d.avg_speed_mph, 0, "mph") : "—"}
        />
        <Field
          label="Max speed"
          value={d.max_speed_mph > 0 ? num(d.max_speed_mph, 0, "mph") : "—"}
        />
        <Field label="SoC" value={`${pct(d.start_soc_pct, 0)} → ${pct(d.end_soc_pct, 0)}`} />
      </div>
      {q.isError ? (
        <p className="mt-2 text-[11px] text-red-400/70">
          Live-drive fetch failed: {String(q.error)}
        </p>
      ) : null}
    </div>
  );
}

// pickImageForState chooses which configurator angle best
// illustrates the current vehicle state. Rivian's real placement
// strings look like `side-exterior-3qfront-driver`,
// `side-exterior-3qrear-driver`, `side-exterior-driver`,
// `front-exterior`, `rear-exterior`, `interior-cabin-driver`,
// `overhead-exterior`, etc. — NOT the short single-word tags we
// used earlier. Match by substring tokens so we actually hit
// something instead of silently falling through to images[0].
//
// State → preferred order:
//
//   charging      → 3qfront (full vehicle, plugged look) → side
//   driving       → side profile → 3qfront
//   frunk open    → front
//   liftgate/tonneau → rear → 3qrear
//   doors open    → side
//   default       → 3qfront (marketing hero), then side, 3qrear, front, rear
//
// Interior / overhead / side-charging are deliberately NOT in any
// preferred list — they look truncated at card size. They only
// appear as last-resort fallbacks.
function pickImageForState(
  images: readonly { url: string; placement?: string }[] | undefined,
  state: VehicleState | undefined,
): string {
  if (!images || images.length === 0) return "";

  // Tokens to try in order. Each entry matches any placement that
  // contains the substring *and* is an exterior shot (skips
  // `interior-cabin-*`). First match wins.
  const wants: string[] = [];
  if (state) {
    // Gate charging imagery on the real plug indicator: charger_state
    // sticks at 'charging_ready' for hours after unplug (see v0.3.48),
    // which would otherwise keep the side-charging / 3qfront crop
    // pinned on a parked unplugged car.
    const plugSt = (state.charger_status || "").toLowerCase();
    const plugged = plugSt.startsWith("chrgr_sts_connected");
    const cs = (state.charger_state || "").toLowerCase();
    const charging = plugged && (cs.startsWith("charging_") || cs === "waiting_on_charger");
    const driving = ["D", "R", "N"].includes((state.gear || "").toUpperCase());

    if (charging) wants.push("charging", "3qfront", "side-exterior", "3qrear");
    else if (driving) wants.push("side-exterior", "3qfront");
    if (!state.frunk_closed) wants.push("front-exterior", "3qfront");
    if (!state.liftgate_closed || !state.tonneau_closed) {
      wants.push("rear-exterior", "3qrear");
    }
    if (!state.doors_closed) wants.push("side-exterior");
  }
  // Universal fallback chain: marketing hero first, then progressively
  // wider angles, then anything exterior, then anything at all.
  wants.push("3qfront", "side-exterior", "3qrear", "front-exterior", "rear-exterior");

  for (const want of wants) {
    const hit = images.find((img) => {
      const p = (img.placement || "").toLowerCase();
      return p.includes(want) && !p.includes("interior");
    });
    if (hit) return hit.url;
  }
  // Last resort: any exterior shot.
  const anyExt = images.find((img) =>
    (img.placement || "").toLowerCase().includes("exterior") &&
    !(img.placement || "").toLowerCase().includes("interior"),
  );
  if (anyExt) return anyExt.url;
  return images[0].url;
}

function formatDuration(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds <= 0) return "—";
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = Math.floor(totalSeconds % 60);
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

function formatPrice(price: string, currency: string): string {
  // Rivian returns the price as a stringified number (e.g. "2.85"). Pass
  // it through verbatim and tack on the currency code if non-empty. The
  // feed sometimes returns "0" for free/home sessions — leave that as-is
  // so it's obvious the charger isn't billing.
  if (!price) return "—";
  if (!currency) return price;
  return `${price} ${currency}`;
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
    // "connected_no_power" (cable in, no draw — e.g. paused session,
    // complete session, or an AC charger that hasn't handshaken yet)
    // and the older "connected_no_chrg" variant both render as a
    // plain "connected" — the fact that there's no power is already
    // obvious from the kW field next to it, and the verbose
    // "plugged · no power" / "connected no chrg" readouts are the
    // kind of jargon the Rivian app hides.
    case "chrgr_sts_connected_no_power":
    case "chrgr_sts_connected_no_chrg":
      return "connected";
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
  if (v === "true" || v === "on" || v === "active" || v === "1") return "yes";
  if (v === "false" || v === "off" || v === "inactive" || v === "0") return "no";
  return formatTitle(s);
}

function formatTitle(s: string): string {
  if (!s) return "—";
  return s.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}

// formatGear expands the single-letter gear code into the
// human-readable term. Rivian's VehicleState publishes gear as
// "P"/"D"/"R"/"N" (plus blank while the ECU is waking); anything
// that isn't in the map falls through to the raw value so new
// firmware values don't silently render as "—".
function formatGear(g: string): string {
  const map: Record<string, string> = {
    P: "Park",
    D: "Drive",
    R: "Reverse",
    N: "Neutral",
  };
  if (!g) return "—";
  return map[g.toUpperCase()] ?? g;
}

// formatOtaAvailable renders the pending-update slot. Rivian reports
// "0.0.0" (and occasionally an empty string) when there's no pending
// build, which the raw string would otherwise surface as a confusing
// version label. Treat both as "no update", and only echo the real
// version back when it differs from the installed one.
function formatOtaAvailable(available: string, current: string): string {
  if (!available || available === "0.0.0") return "no";
  if (available === current) return "up-to-date";
  return available;
}

// formatTwelveVolt collapses Rivian's verbose 12-V health states
// ("NORMAL_OPERATION", "LOW_BATTERY", etc.) to a one-word readout
// that fits the live panel's data-dense grid. Unknown values pass
// through formatTitle so we don't lose information on a surprise
// enum.
function formatTwelveVolt(s: string): string {
  if (!s) return "—";
  const v = s.toLowerCase();
  if (v === "normal_operation" || v === "normal") return "Normal";
  if (v === "low_battery" || v === "low") return "Low";
  if (v === "critical_battery" || v === "critical") return "Critical";
  if (v === "unknown") return "—";
  return formatTitle(s);
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
