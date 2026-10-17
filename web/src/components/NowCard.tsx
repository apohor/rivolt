import { useEffect, useMemo, useState } from "react";
import { displayTimeZone } from "../lib/format";
import { formatScheduleStart, nextScheduledStartMin } from "../lib/chargingSchedule";
import { ArcGauge } from "./ArcGauge";
import { PowerSpark } from "./PowerSpark";
import { useUiNext } from "../lib/uiNext";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useImpersonationTarget } from "../lib/impersonation";
import { backend, type VehicleState } from "../lib/api";
import {
  advanceTripDistance,
  computeNow,
  formatEta,
  isChargingActive,
  isDrivingGear,
  freshnessLine,
  nowAlerts,
  statusPill,
  SLEEP_HYSTERESIS_SEC,
  type NowView,
  type OdoTrack,
} from "../lib/nowState";
import { formatMoney } from "../lib/ledger";
import { formatDistance, usePreferences, type DistanceUnit } from "../lib/preferences";

// The Now card (ux-slice-spec §3): one state-driven hero for the
// selected vehicle. Composed from /api/state plus a few cached
// endpoints, never from Rivian's live-session feed, so it renders in
// replay mode. Fields the source never provides are hidden, not shown
// as "—" (NQ15).

type Tracker = {
  asleepSinceMs: number | null;
  peakKw: number;
  odo: OdoTrack | null;
  lastAt: string;
};

const EMPTY: Tracker = {
  asleepSinceMs: null,
  peakKw: 0,
  odo: null,
  lastAt: "",
};

// Folds each new frame into the client-side observations the state
// machine needs: how long it's been asleep, where this charge began,
// the peak kW, and a monotonic odometer.
function advance(t: Tracker, s: VehicleState, nowMs: number): Tracker {
  if (s.at === t.lastAt) return t;
  const next: Tracker = { ...t, lastAt: s.at };
  const asleep = (s.power_state || "").toLowerCase() === "sleep" && !isDrivingGear(s) && !isChargingActive(s);
  // The frame itself says the truck was asleep at s.at, so a client
  // joining an asleep truck doesn't restart the 2-minute debounce.
  const frameMs = new Date(s.at).getTime();
  // First sight of an already-asleep truck shows Asleep at once; the
  // 2-minute debounce only guards transitions watched happening.
  const seed = (Number.isFinite(frameMs) ? frameMs : nowMs) - SLEEP_HYSTERESIS_SEC * 1000;
  next.asleepSinceMs = asleep ? (t.asleepSinceMs ?? (t.lastAt === "" ? seed : (Number.isFinite(frameMs) ? frameMs : nowMs))) : null;
  if (isChargingActive(s)) {
    next.peakKw = Math.max(t.peakKw, s.charger_power_kw || 0);
  } else if (!s.charger_status?.toLowerCase().startsWith("chrgr_sts_connected")) {
    next.peakKw = 0;
  }
  const odoMi = s.odometer_km / 1.609344;
  if (isDrivingGear(s) && odoMi > 0) {
    const atMs = new Date(s.at).getTime() || nowMs;
    next.odo = advanceTripDistance(t.odo, odoMi, atMs);
  } else {
    next.odo = null;
  }
  return next;
}

export default function NowCard({
  vehicleID,
  vehicleName,
  packKwh,
  showTelemetryLink = true,
}: {
  vehicleID: string;
  vehicleName: string;
  packKwh: number | null;
  // /live needs a signed-in session, so the public demo turns it off.
  showTelemetryLink?: boolean;
}) {
  // Live is hidden while impersonating (matches AppLayout).
  const impersonating = useImpersonationTarget() != null;
  const { distanceUnit } = usePreferences();
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNowMs(Date.now()), 15_000);
    return () => window.clearInterval(id);
  }, []);

  const stateQ = useQuery({
    queryKey: ["rivian", "state", vehicleID],
    queryFn: () => backend.vehicleState(vehicleID),
    refetchInterval: 15_000,
    retry: 1,
  });
  const homeQ = useQuery({
    queryKey: ["settings", "home-location"],
    queryFn: () => backend.homeLocationGet(),
    staleTime: 5 * 60_000,
    retry: 1,
  });
  const chargingQ = useQuery({
    queryKey: ["settings", "charging"],
    queryFn: () => backend.getChargingSettings(),
    staleTime: 5 * 60_000,
    retry: 1,
  });
  const sleepSince = useMemo(() => new Date(Date.now() - 2 * 86_400_000).toISOString(), []);
  const sleepQ = useQuery({
    queryKey: ["sleepActivity", "now", vehicleID],
    queryFn: () => backend.sleepActivity(sleepSince, vehicleID),
    staleTime: 5 * 60_000,
    retry: 1,
  });

  const [tracker, setTracker] = useState<Tracker>(EMPTY);
  const state = stateQ.data;
  useEffect(() => {
    if (state) setTracker((t) => advance(t, state, Date.now()));
  }, [state]);
  // Charging power history for the ui_theme_next sparkline: one reading per
  // successful poll while charging, cleared when the session ends.
  const [powerHist, setPowerHist] = useState<number[]>([]);
  useEffect(() => {
    const kw = state?.charger_power_kw;
    if (typeof kw === "number" && kw > 0) setPowerHist((h) => [...h.slice(-47), kw]);
    else setPowerHist((h) => (h.length ? [] : h));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stateQ.dataUpdatedAt]);

  // GET /now: the server's state, freshness inputs and charge baseline.
  // Null when absent (older server / flag off): fall back to the
  // client-derived path below.
  const nowQ = useQuery({
    queryKey: ["vehicle", "now", vehicleID],
    queryFn: () => backend.vehicleNow(vehicleID),
    refetchInterval: 15_000,
    retry: 0,
  });
  const server = nowQ.data ?? null;

  // Server-side baselines: the open charge row carries the session's
  // true start SoC and peak kW (the client only sees frames since the
  // page opened). Newest row counts only if it's contiguous with now.
  const chargesQ = useQuery({
    queryKey: ["charges", "recent", vehicleID],
    queryFn: () => backend.recentCharges(3),
    // Only needed when /now isn't available.
    enabled: !!state && isChargingActive(state) && nowQ.isFetched && !server,
    refetchInterval: 60_000,
    retry: 0,
  });
  const openCharge = useMemo(() => {
    if (!state) return null;
    const at = new Date(state.at).getTime();
    const row = (chargesQ.data ?? []).find((c) => c.VehicleID === vehicleID);
    if (!row) return null;
    const end = new Date(row.EndedAt).getTime();
    // Always contiguous with now: prod has closed rows still marked
    // charging_active (MQ5), so FinalState alone isn't enough.
    const start = new Date(row.StartedAt).getTime();
    const contiguous = Number.isFinite(end) && end > 0 && at - end < 10 * 60_000 && at >= end - 60_000;
    const stillOpen =
      row.FinalState === "charging_active" &&
      (!Number.isFinite(end) || end <= 0 || row.EndedAt.startsWith("0001")) &&
      Number.isFinite(start) && at - start >= 0 && at - start < 72 * 3_600_000;
    const open = contiguous || stillOpen;
    return open ? row : null;
  }, [chargesQ.data, state, vehicleID]);

  const view = useMemo<NowView | null>(() => {
    if (!state) return null;
    const home = homeQ.data?.set ? homeQ.data : null;
    return computeNow({
      state,
      nowMs,
      asleepSinceMs: tracker.asleepSinceMs,
      socAtChargeStart: server?.charge ? server.charge.soc_start : openCharge ? openCharge.StartSoCPct : null,
      peakKw: server?.charge
        ? Math.max(server.charge.peak_kw, tracker.peakKw)
        : openCharge
          ? Math.max(openCharge.MaxPowerKW, tracker.peakKw)
          : null,
      server,
      packKwh,
      homeRate: chargingQ.data?.home_price_per_kwh ?? null,
      home,
    });
  }, [state, nowMs, tracker, openCharge, server, packKwh, homeQ.data, chargingQ.data]);

  const next = useUiNext(); // a hook: must run before the early returns below
  const driveQ = useQuery({
    queryKey: ["rivian", "live-drive", vehicleID],
    queryFn: () => backend.liveDrive(vehicleID),
    enabled: view?.kind === "driving",
    refetchInterval: 15_000,
    retry: 0,
  });

  if (stateQ.isLoading) {
    return (
      <section aria-label="Now" className="rounded-xl border border-neutral-800 bg-neutral-900/60 p-5">
        <div className="h-24 animate-pulse rounded bg-neutral-800/60" />
      </section>
    );
  }
  if (!state || !view) {
    return (
      <section aria-label="Now" className="rounded-xl border border-neutral-800 bg-neutral-900/60 p-5">
        <p className="text-sm text-neutral-300">Rivolt can't reach {vehicleName} right now.</p>
        <p className="mt-1 text-xs text-neutral-500">Reconnecting. The last known values appear here when available.</p>
      </section>
    );
  }

  const failed = (sleepQ.data ?? []).slice(-1)[0]?.failed_sleeps ?? 0;
  const alerts = nowAlerts({
    view,
    state,
    vehicleName,
    failedSleepsToday: failed,
    failedSleeps6h: server ? server.failed_sleeps_6h : null,
    tireMinBar: server?.tire_pressure_min_bar ?? null,
  });
  const currency = chargingQ.data?.home_currency || "USD";
  const offline = view.kind === "offline";
  // Plugged in but not charging, and a schedule for today that applies here is still to
  // start: say when ("charging starts 11:00 PM (schedule)"). Not when it is holding at its limit.
  const schedStart =
    view.kind === "plugged" && !view.atLimit ? nextScheduledStartMin(state?.charging_schedules, new Date(), displayTimeZone()) : null;
  const scheduleText = schedStart != null ? `charging starts ${formatScheduleStart(schedStart)} (schedule)` : null;
  const pill = statusPill(view);
  const critical = view.critical && !offline;
  const tone = offline ? "text-neutral-500" : critical ? "text-rose-300" : "text-neutral-100";
  const liveKind = view.kind === "charging" ? "charging" : view.kind === "driving" ? "driving" : undefined;

  return (
    <section aria-label="Now" data-ui-area="hero" className="space-y-2">
      <div
        data-ui-hero="true"
        data-live={liveKind}
        className={`rounded-xl border bg-neutral-900/60 p-5 ${
          critical ? "border-rose-500/50" : "border-neutral-800"
        }`}
      >
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-xs uppercase tracking-wide text-neutral-500">{vehicleName}</p>
            <p className="mt-0.5 text-sm text-neutral-300" data-testid="now-headline">
              {headline(view, distanceUnit)}
              {scheduleText && (
                <span data-testid="now-schedule" className="text-neutral-400">
                  {" · "}
                  {scheduleText}
                </span>
              )}
            </p>
          </div>
          <span
            data-testid="now-pill"
            className={`shrink-0 rounded-full border px-2 py-0.5 text-[11px] ${
              pill.tone === "live"
                ? "border-emerald-500/30 text-emerald-300"
                : pill.tone === "warn"
                  ? "border-amber-500/40 text-amber-300"
                  : "border-neutral-700 text-neutral-400"
            }`}
          >
            {pill.text}
          </span>
        </div>

        {next ? (
          <div className="mt-3 flex items-center gap-5">
            <ArcGauge
              value={view.soc}
              label={view.soc > 0 ? `Battery ${Math.round(view.soc)} percent` : "Battery level unknown"}
              live={view.kind === "charging"}
              tone={offline ? "idle" : critical ? "critical" : "ok"}
              limit={view.kind === "charging" || view.kind === "plugged" ? view.limitPct : null}
            >
              {(shown) => (
                <span className={`font-semibold leading-none tabular-nums ${tone}`}>
                  <span className="text-4xl">{view.soc > 0 ? Math.round(shown) : "—"}</span>
                  <span className="ml-0.5 text-lg font-normal text-neutral-400">%</span>
                </span>
              )}
            </ArcGauge>
            <div className="min-w-0 space-y-3">
              {view.kind === "driving" && view.speedMph != null && (
                <div>
                  <p className="ui-label">Speed</p>
                  <p className={`text-3xl font-semibold leading-none tabular-nums ${tone}`}>
                    {Math.round(view.speedMph)}
                    <span className="ml-1 text-sm font-normal text-neutral-400">mph</span>
                  </p>
                </div>
              )}
              {(view.kind === "charging" || view.kind === "plugged") && view.limitPct != null && (
                <div>
                  <p className="ui-label">Charge limit</p>
                  <p className="text-2xl font-semibold leading-none tabular-nums text-neutral-100">
                    {Math.round(view.limitPct)}
                    <span className="ml-1 text-sm font-normal text-neutral-400">%</span>
                  </p>
                </div>
              )}
              {view.rangeMi != null && (
                <div>
                  <p className="ui-label">Range</p>
                  <p className="text-2xl font-semibold leading-none tabular-nums text-neutral-100">
                    {Math.round(view.rangeMi)}
                    <span className="ml-1 text-sm font-normal text-neutral-400">mi</span>
                  </p>
                </div>
              )}
            </div>
          </div>
        ) : (
        <div className="mt-3 flex flex-wrap items-baseline gap-x-4 gap-y-1">
          {view.kind === "driving" && view.speedMph != null ? (
            <>
              <span className={`text-5xl font-semibold tabular-nums ${tone}`}>
                {Math.round(view.speedMph)}
                <span className="ml-1 text-lg font-normal text-neutral-500">mph</span>
              </span>
              <span className="text-lg tabular-nums text-neutral-300">{Math.round(view.soc)}%</span>
            </>
          ) : (
            <>
              <span className={`text-5xl font-semibold tabular-nums ${tone}`}>
                {view.soc > 0 ? Math.round(view.soc) : "—"}
                <span className="ml-0.5 text-2xl font-normal text-neutral-500">%</span>
              </span>
              {view.rangeMi != null && (
                <span className="text-lg tabular-nums text-neutral-400">{Math.round(view.rangeMi)} mi</span>
              )}
            </>
          )}
        </div>
        )}

        {next && view.kind === "charging" && powerHist.length >= 2 && (
          <div className="mt-3 flex items-center gap-3">
            <span className="ui-label shrink-0">Power</span>
            <PowerSpark values={powerHist} />
            <span className="shrink-0 text-lg font-semibold tabular-nums text-neutral-100">
              {Math.round(powerHist[powerHist.length - 1])}
              <span className="ml-1 text-xs font-normal text-neutral-400">kW</span>
            </span>
          </div>
        )}

        {!next && (view.kind === "charging" || view.kind === "plugged") && view.soc > 0 && (
          <LimitBar soc={view.soc} limit={view.limitPct} />
        )}

        <dl className="mt-3 grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
          {details(view, tracker, driveQ.data ?? null, nowMs, currency)
            // the sparkline row already shows the live power
            .filter((d) => !(next && view.kind === "charging" && powerHist.length >= 2 && d.k === "Power"))
            .map((d) => (
            <div key={d.k} className="contents">
              <dt className="text-neutral-500">{d.k}</dt>
              {/* Mono only for short numeric readouts (a kW, a time left); text stays sans so it can wrap. */}
              <dd className="text-right tabular-nums text-neutral-200">{d.v}</dd>
            </div>
          ))}
        </dl>

        <div className="mt-3 flex items-center justify-between gap-3 text-xs text-neutral-500">
          <p data-testid="now-freshness">{freshnessLine(view)}</p>
          {!impersonating && showTelemetryLink && (
            <Link to="/live" className="text-neutral-300 hover:text-emerald-300">
              Full telemetry →
            </Link>
          )}
        </div>
      </div>

      {alerts.map((a) => {
        const cls =
          a.tone === "rose"
            ? "border-rose-500/40 bg-rose-950/30 text-rose-200"
            : "border-amber-500/30 bg-amber-950/20 text-amber-200";
        const body = <span>{a.text}</span>;
        return a.to ? (
          <Link key={a.id} to={a.to} className={`block rounded-lg border px-3 py-2 text-sm ${cls}`}>
            {body}
          </Link>
        ) : (
          <div key={a.id} role="status" className={`rounded-lg border px-3 py-2 text-sm ${cls}`}>
            {body}
          </div>
        );
      })}
    </section>
  );
}

function headline(v: NowView, unit: DistanceUnit): string {
  switch (v.kind) {
    case "offline":
      return "Not heard from recently";
    case "driving":
      return "Driving";
    case "charging":
      if (v.starting) return "Starting to charge…";
      if (v.chargeFlavor === "dcfc") return "Fast charging";
      if (v.chargeFlavor === "home_l2") return "Charging at home";
      return "Charging";
    case "plugged":
      return v.atLimit ? `Plugged in, holding ${v.limitPct ?? ""}%`.replace(/ %$/, "") : "Plugged in";
    case "asleep":
      return "Asleep";
    default:
      return placeLine(v, unit) ?? "Parked";
  }
}

export function placeLine(v: NowView, unit: DistanceUnit): string | null {
  if (v.place === "home") return "Parked at home";
  if (v.place === "away" && v.kmFromHome != null) {
    return `Parked · ${formatDistance(v.kmFromHome, unit, { fine: true })} from home`;
  }
  return null;
}

function details(
  v: NowView,
  t: Tracker,
  drive: { distance_mi: number; start_odometer_mi: number; soc_used_pct: number; energy_used_kwh: number } | null,
  nowMs: number,
  currency: string,
): { k: string; v: string }[] {
  const rows: { k: string; v: string }[] = [];
  if (v.kind === "driving") {
    // Monotonic distance from the client's clamped odometer track; the
    // server's LiveDrive number is the fallback.
    // Server-owned start odometer; the client's monotonic max only
    // clamps on top. Hidden until the server has the drive.
    if (!drive) return rows;
    const tripMi = Math.max(0, (t.odo ? t.odo.max : 0) - drive.start_odometer_mi, drive.distance_mi);
    if (tripMi > 0) rows.push({ k: "Trip so far", v: `${tripMi.toFixed(1)} mi` });
    // Suppressed until it can't be absurd (spec §3.2b). Miles and kWh
    // are both the server's whole-drive figures.
    if (drive.distance_mi >= 1 && drive.soc_used_pct >= 1 && drive.energy_used_kwh > 0) {
      rows.push({ k: "Efficiency", v: `${(drive.distance_mi / drive.energy_used_kwh).toFixed(1)} mi/kWh` });
    }
    return rows;
  }
  if (v.kind === "charging") {
    if (v.powerKw != null) rows.push({ k: "Power", v: `${v.powerKw.toFixed(v.powerKw < 10 ? 1 : 0)} kW` });
    if (v.etaHours != null) rows.push({ k: "Time left", v: `${formatEta(v.etaHours, nowMs)} · est.` });
    if (v.energyKwh != null && v.energyKwh > 0) rows.push({ k: "Added", v: `≈ ${v.energyKwh.toFixed(1)} kWh` });
    if (v.costSoFar) rows.push({ k: "Cost so far", v: `${formatMoney(v.costSoFar.amount, currency)} · home rate` });
    else if (v.priceUnknown) rows.push({ k: "Price", v: "Unknown, add it after" });
    else if (v.chargeFlavor === "dcfc") rows.push({ k: "Price", v: "Estimated when it ends" });
  }
  return rows;
}

function LimitBar({ soc, limit }: { soc: number; limit: number | null }) {
  const s = Math.max(0, Math.min(100, soc));
  return (
    <div className="mt-3" aria-hidden>
      <div className="relative h-2 rounded-full bg-neutral-800">
        <div className="h-2 rounded-full bg-emerald-500" style={{ width: `${s}%` }} />
        {limit != null && (
          <div className="absolute -top-1 h-4 w-0.5 bg-neutral-200" style={{ left: `${Math.min(100, limit)}%` }} />
        )}
      </div>
      {limit != null && <p className="mt-1 text-right text-[11px] text-neutral-500">Limit {Math.round(limit)}%</p>}
    </div>
  );
}
