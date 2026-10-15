// Pure state machine for the Now card (ux-slice-spec §3). Composes the
// card's view from /api/state alone, so it renders fully when the live
// Rivian session feed is unavailable (replay / preview). Everything
// time-dependent takes `nowMs` so it is unit-testable.

import type { NowSnapshot, VehicleState } from "./api";

export type NowKind = "offline" | "driving" | "charging" | "plugged" | "asleep" | "parked";
export type ChargeFlavor = "home_l2" | "away_ac" | "dcfc";
export type Place = "home" | "away" | "unknown";

// Spec §3.3: show "stale" / "offline" after these ages (seconds).
const FRESHNESS: Record<"driving" | "charging" | "parked" | "asleep", { stale: number; offline: number }> = {
  driving: { stale: 30, offline: 180 },
  charging: { stale: 120, offline: 600 },
  parked: { stale: 300, offline: 900 },
  // 45 min: prod polls a sleeping truck every ~30 min.
  asleep: { stale: 1800, offline: 2700 },
};

// A sleep shorter than this doesn't change the hero (spec §3.1, NQ2).
export const SLEEP_HYSTERESIS_SEC = 120;
// Charger kW at or below this reads as "Starting…" (NQ4).
export const MIN_CHARGING_KW = 0.3;
export const DCFC_PEAK_KW = 50;
export const CRITICAL_SOC_PCT = 10;
export const LOW_TIRE_BAR = 2.2;
// Matches the ledger's home geofence so Now and Money agree.
const HOME_RADIUS_M = 250;

export type NowInput = {
  state: VehicleState;
  nowMs: number;
  // When the vehicle first reported power_state=sleep in this
  // uninterrupted run (client-observed), or null.
  asleepSinceMs: number | null;
  // SoC when the current charge began. Server-seeded (open charge row);
  // null means unknown, so energy and cost are hidden.
  socAtChargeStart: number | null;
  // Highest kW of this charge, or null when unknown (client joined
  // mid-session with no server row). Unknown away-from-home sessions
  // are never labelled AC/unpriced.
  peakKw: number | null;
  packKwh: number | null;
  homeRate: number | null;
  home: { latitude: number; longitude: number } | null;
  // GET /now, when the server has it. Its `state` is taken as-is.
  server?: NowSnapshot | null;
};

export type NowView = {
  kind: NowKind;
  // True when the last frame is older than the stale limit but not yet offline.
  stale: boolean;
  ageSec: number;
  lastContactMs: number;
  soc: number;
  rangeMi: number | null;
  limitPct: number | null;
  critical: boolean;
  place: Place;
  kmFromHome: number | null;
  // Charging
  chargeFlavor: ChargeFlavor | null;
  // null unless actually charging with kW above the floor (NQ3/NQ4).
  powerKw: number | null;
  starting: boolean;
  etaHours: number | null;
  energyKwh: number | null;
  costSoFar: { amount: number; basis: "home_rate" } | null;
  priceUnknown: boolean;
  atLimit: boolean;
  // Driving
  speedMph: number | null;
  // From /now (null when absent).
  replay: boolean;
  locationFixMs: number | null;
  stateSinceMs: number | null;
  // SoC change since the current state began (parked/plugged/asleep).
  socChange: number | null;
};

const KM_PER_MI = 1.609344;

export function isPlugged(s: VehicleState): boolean {
  return (s.charger_status || "").toLowerCase().startsWith("chrgr_sts_connected");
}

export function isDrivingGear(s: VehicleState): boolean {
  // Gear, never speed alone: speed noise appears in Park (NQ10).
  const g = (s.gear || "").toUpperCase();
  return g === "D" || g === "R";
}

export function isChargingActive(s: VehicleState): boolean {
  return isPlugged(s) && (s.charger_state || "") === "charging_active";
}

export function haversineM(a: { latitude: number; longitude: number }, b: { latitude: number; longitude: number }): number {
  const R = 6371000;
  const rad = (d: number) => (d * Math.PI) / 180;
  const dLat = rad(b.latitude - a.latitude);
  const dLon = rad(b.longitude - a.longitude);
  const h =
    Math.sin(dLat / 2) ** 2 +
    Math.cos(rad(a.latitude)) * Math.cos(rad(b.latitude)) * Math.sin(dLon / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(h));
}

// rawKind is the precedence ladder without freshness or hysteresis.
const SERVER_STATES = new Set(["driving", "charging", "plugged", "parked", "asleep"]);

function rawKind(s: VehicleState, server?: NowSnapshot | null): Exclude<NowKind, "offline"> {
  // The server decides the state when it can (one place for the rules).
  if (server && SERVER_STATES.has(server.state)) return server.state as Exclude<NowKind, "offline">;
  if (isDrivingGear(s)) return "driving";
  if (isChargingActive(s)) return "charging";
  if (isPlugged(s)) return "plugged";
  if ((s.power_state || "").toLowerCase() === "sleep") return "asleep";
  return "parked";
}

export function computeNow(i: NowInput): NowView {
  const s = i.state;
  const atMs = new Date(s.at).getTime();
  const lastContactMs = Number.isFinite(atMs) ? atMs : 0;
  const ageSec = lastContactMs > 0 ? Math.max(0, (i.nowMs - lastContactMs) / 1000) : Infinity;

  let kind: Exclude<NowKind, "offline"> = rawKind(s, i.server);
  // 2-minute sleep debounce: a brief sleep reads as parked.
  if (kind === "asleep") {
    const sleptSec = i.asleepSinceMs == null ? 0 : (i.nowMs - i.asleepSinceMs) / 1000;
    if (sleptSec < SLEEP_HYSTERESIS_SEC) kind = "parked";
  }

  const fKey = kind === "plugged" ? "parked" : kind;
  const lim = FRESHNESS[fKey];
  const offline = ageSec > lim.offline;
  const stale = !offline && ageSec > lim.stale;

  const soc = Number.isFinite(s.battery_level_pct) ? s.battery_level_pct : 0;
  const rangeMi = s.distance_to_empty > 0 ? s.distance_to_empty / KM_PER_MI : null;
  const limitPct = s.charge_target_pct > 0 ? s.charge_target_pct : null;

  let place: Place = "unknown";
  let kmFromHome: number | null = null;
  const hasFix = Number.isFinite(s.latitude) && Number.isFinite(s.longitude) && (s.latitude !== 0 || s.longitude !== 0);
  if (i.home && hasFix) {
    const m = haversineM(i.home, s);
    kmFromHome = m / 1000;
    place = m <= HOME_RADIUS_M ? "home" : "away";
  }

  const charging = kind === "charging" && !offline;
  const rawKw = s.charger_power_kw;
  const powerKw = charging && rawKw > MIN_CHARGING_KW ? rawKw : null;
  const starting = charging && powerKw == null;

  const peak = Math.max(i.peakKw ?? 0, powerKw ?? 0);
  let chargeFlavor: ChargeFlavor | null = null;
  if (kind === "charging" || kind === "plugged") {
    if (peak >= DCFC_PEAK_KW) chargeFlavor = "dcfc";
    else if (place === "home") chargeFlavor = "home_l2";
    // Away AC only when the whole session's peak is known: a taper
    // below 50 kW seen from mid-session could be a Supercharger.
    else if (place === "away" && i.peakKw != null) chargeFlavor = "away_ac";
  }

  const target = limitPct ?? 100;
  let etaHours: number | null = null;
  if (charging && powerKw != null && i.packKwh && target > soc) {
    etaHours = ((target - soc) / 100) * i.packKwh / powerKw;
  }

  let energyKwh: number | null = null;
  if (charging && i.socAtChargeStart != null && i.packKwh) {
    energyKwh = Math.max(0, ((soc - i.socAtChargeStart) / 100) * i.packKwh);
  }

  const costSoFar =
    charging && chargeFlavor === "home_l2" && energyKwh != null && i.homeRate != null && i.homeRate > 0
      ? { amount: energyKwh * i.homeRate, basis: "home_rate" as const }
      : null;
  const priceUnknown = charging && chargeFlavor === "away_ac";

  return {
    kind: offline ? "offline" : kind,
    stale,
    ageSec,
    lastContactMs,
    soc,
    rangeMi,
    limitPct,
    critical: soc > 0 && soc < CRITICAL_SOC_PCT,
    place,
    kmFromHome,
    chargeFlavor,
    powerKw,
    starting,
    etaHours,
    energyKwh,
    costSoFar,
    priceUnknown,
    atLimit: limitPct != null && soc >= limitPct - 1.5,
    speedMph: kind === "driving" && s.speed_kph >= 0 ? s.speed_kph / KM_PER_MI : null,
    replay: i.server?.connection === "replay",
    locationFixMs: i.server?.location_fix_at ? Date.parse(i.server.location_fix_at) || null : null,
    stateSinceMs: i.server?.state_since ? Date.parse(i.server.state_since) || null : null,
    socChange:
      i.server?.soc_at_state_start != null && (kind === "parked" || kind === "plugged" || kind === "asleep")
        ? soc - i.server.soc_at_state_start
        : null,
  };
}

// Monotonic trip distance (spec §3.2b): never decreases on screen, and a
// forward jump faster than 120 mph is held until confirmed by a later
// sample. `prev` is the last accepted value, `pending` an unconfirmed jump.
export type OdoTrack = { max: number; pending: number | null; at: number };

export function advanceTripDistance(
  prev: OdoTrack | null,
  reading: number,
  atMs: number,
): OdoTrack {
  if (!Number.isFinite(reading)) return prev ?? { max: 0, pending: null, at: atMs };
  if (!prev) return { max: reading, pending: null, at: atMs };
  const dtH = Math.max((atMs - prev.at) / 3_600_000, 1 / 3600);
  if (reading <= prev.max) return { ...prev, at: atMs };
  const mph = (reading - prev.max) / dtH;
  if (mph <= 120) return { max: reading, pending: null, at: atMs };
  // Implausible jump: accept only if a following sample confirms it.
  if (prev.pending != null && reading >= prev.pending) {
    return { max: reading, pending: null, at: atMs };
  }
  return { ...prev, pending: reading, at: atMs };
}

export type NowAlert = { id: string; tone: "rose" | "amber"; text: string; to?: string };

export function nowAlerts(opts: {
  view: NowView;
  state: VehicleState;
  vehicleName: string;
  // Today's sleep record: how many times the car fell asleep, and how
  // many of those ended inside a minute.
  failedSleepsToday: number;
  // From /now: failed sleeps in the last 6 h, and the min tire bar
  // (preferred over the per-wheel fields, which replay doesn't carry).
  failedSleeps6h?: number | null;
  tireMinBar?: number | null;
}): NowAlert[] {
  const out: NowAlert[] = [];
  const { view, state, vehicleName, failedSleepsToday, failedSleeps6h, tireMinBar } = opts;
  if (view.critical && view.kind !== "offline") {
    out.push({ id: "critical-soc", tone: "rose", text: `Battery at ${Math.round(view.soc)}%. Plan a charge soon.` });
  }
  // The server's sub-day count when present, else today's rollup.
  if (failedSleeps6h != null) {
    if (failedSleeps6h >= 3) {
      out.push({
        id: "waking",
        tone: "amber",
        text: `${vehicleName} woke up ${failedSleeps6h} times in the last 6 hours.`,
        to: "/truck",
      });
    }
  } else if (failedSleepsToday >= 3) {
    out.push({
      id: "waking",
      tone: "amber",
      text: `${vehicleName} woke up ${failedSleepsToday} times today.`,
      to: "/truck",
    });
  }
  const tires =
    tireMinBar != null && tireMinBar > 0
      ? [tireMinBar]
      : [
          state.tire_pressure_fl_bar,
          state.tire_pressure_fr_bar,
          state.tire_pressure_rl_bar,
          state.tire_pressure_rr_bar,
        ].filter((v) => Number.isFinite(v) && v > 0);
  if (tires.length > 0) {
    const low = Math.min(...tires);
    if (low < LOW_TIRE_BAR) {
      out.push({ id: "tire", tone: "amber", text: `Lowest tire ${low.toFixed(1)} bar.` });
    }
  }
  return out.slice(0, 2);
}

export function formatEta(hours: number, nowMs: number): string {
  if (hours < 1) return `~${Math.max(1, Math.round(hours * 60))} min`;
  if (hours < 24) {
    const h = Math.floor(hours);
    const m = Math.round((hours - h) * 60);
    return m === 0 ? `~${h} h` : `~${h} h ${m} m`;
  }
  const done = new Date(nowMs + hours * 3_600_000);
  const day = done.toLocaleDateString(undefined, { weekday: "short" });
  const t = done.toLocaleTimeString(undefined, { hour: "numeric" });
  return `~${Math.round(hours)} h (${day} ${t})`;
}

export function formatAgo(ageSec: number): string {
  if (!Number.isFinite(ageSec)) return "a while ago";
  const m = Math.floor(ageSec / 60);
  if (m < 1) return "just now";
  if (m < 60) return `${m} min ago`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} h ago`;
  return `${Math.floor(h / 24)} d ago`;
}

// How long a frame may be and still read "Live" next to an "updated N
// min ago" line. Tighter than the per-state stale limit on purpose, so a
// green Live never sits beside a visibly old timestamp.
export const LIVE_PILL_MAX_SEC = 120;

export type StatusPill = { text: string; tone: "live" | "neutral" | "warn" };

export function statusPill(v: NowView): StatusPill {
  if (v.kind === "offline") return { text: "Offline", tone: "neutral" };
  // A sleeping truck's frames are legitimately minutes apart.
  if (v.kind === "asleep" && !v.stale) return { text: "Asleep", tone: "neutral" };
  if (!v.stale && v.ageSec <= LIVE_PILL_MAX_SEC) return { text: "Live", tone: "live" };
  const t = `Last seen ${formatAgo(v.ageSec)}`;
  return { text: t, tone: v.stale ? "warn" : "neutral" };
}
