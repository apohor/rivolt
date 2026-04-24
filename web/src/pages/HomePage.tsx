import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { Card, ErrorBox } from "../components/ui";
import { BarChart, LineChart } from "../components/charts";
import {
  durationSeconds,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";
import {
  chargeStats,
  driveStats,
  filterByWindow,
  milesPerDay,
  socTrend,
  WINDOW_OPTIONS,
  type WindowKey,
} from "../lib/analytics";
import { collapseRoundTrips } from "../lib/drives";
import { usePreferences } from "../lib/preferences";
import { WindowPicker } from "../components/WindowPicker";

export default function HomePage() {
  const [win, setWin] = useState<WindowKey>("30d");
  const {
    roundTripsEnabled,
    roundTripRadiusMeters,
    roundTripMaxGapMinutes,
  } = usePreferences();

  const drives = useQuery({
    queryKey: ["drives", "all"],
    queryFn: () => backend.allDrives(),
  });
  const charges = useQuery({
    queryKey: ["charges", "all"],
    queryFn: () => backend.allCharges(),
  });

  const all = drives.data ?? [];
  const allC = charges.data ?? [];
  const winDrives = useMemo(() => {
    const filtered = filterByWindow(all, win);
    return roundTripsEnabled
      ? collapseRoundTrips(
          filtered,
          roundTripRadiusMeters,
          roundTripMaxGapMinutes,
        )
      : filtered;
  }, [all, win, roundTripsEnabled, roundTripRadiusMeters, roundTripMaxGapMinutes]);
  const winCharges = useMemo(() => filterByWindow(allC, win), [allC, win]);
  const ds = useMemo(() => driveStats(winDrives), [winDrives]);
  const cs = useMemo(() => chargeStats(winCharges), [winCharges]);

  // Prefer the live vehicle state for the headline SoC — the fallback
  // to the last recorded session is misleading when the car has been
  // driven / charged since the last row landed.
  const rivianStatus = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    staleTime: 30_000,
    retry: 1,
  });
  const vehicles = useQuery({
    queryKey: ["rivian", "vehicles"],
    queryFn: () => backend.vehicles(),
    enabled: !!rivianStatus.data?.authenticated,
    staleTime: 5 * 60_000,
    retry: 1,
  });
  const firstVehicleID = vehicles.data?.[0]?.id;
  const liveState = useQuery({
    queryKey: ["rivian", "state", firstVehicleID ?? ""],
    queryFn: () => backend.vehicleState(firstVehicleID as string),
    enabled: !!firstVehicleID,
    refetchInterval: 60_000,
    retry: 1,
  });
  const sessionSoC = all[0]?.EndSoCPct ?? allC[0]?.EndSoCPct ?? 0;
  const liveSoC = liveState.data?.battery_level_pct ?? 0;
  const batteryValue = liveSoC > 0 ? liveSoC : sessionSoC;
  const batteryLabel = liveSoC > 0 ? "Battery" : "Battery (last seen)";

  const barDays = win === "7d" ? 7 : win === "30d" ? 30 : 60;
  const dailyMiles = useMemo(
    () => milesPerDay(winDrives, barDays),
    [winDrives, barDays],
  );
  const trend = useMemo(
    () => socTrend(winDrives, winCharges),
    [winDrives, winCharges],
  );

  const isError = drives.isError || charges.isError;

  return (
    <div className="space-y-4">
      {/* Hero owns the page header AND the window picker: folding the
          picker into the hero removes the orphan 'Summary · 30 days'
          row that floated between the hero card and the KPI card on
          the previous layout (three competing borders in the first
          viewport). Hero is now the single anchor — its CTAs still
          route to /live and /drives, and a divider + right-aligned
          picker communicate "this scope affects everything below". */}
      <HeroBanner win={win} onWinChange={setWin} />

      {/* KPI row (Battery / Miles / Energy added / Efficiency) sits
          at the top of the data area now — it's the highest-signal
          row on the page. Battery uses the live SoC so it also
          doubles as the "current state" readout that used to live
          in the separate LiveSummary card. Renders even while
          drives/charges are still loading; the derived stats fill
          in once those queries settle. */}
      {isError ? (
        <ErrorBox
          title="Failed to load sessions"
          detail={String(drives.error ?? charges.error)}
        />
      ) : (
        <>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            <Stat label={batteryLabel} value={pct(batteryValue, 0)} />
            <Stat
              label="Miles"
              value={num(ds.miles, 1, "mi")}
              hint={`${ds.count} drives · avg ${num(ds.avgTripMi, 1, "mi")}`}
            />
            <Stat
              label="Energy added"
              value={num(cs.energyKWh, 1, "kWh")}
              hint={`${cs.count} charges · peak ${num(cs.maxPowerKW, 0, "kW")}`}
            />
            <Stat
              label="Efficiency"
              value={
                cs.energyKWh > 0 && ds.miles > 0
                  ? `${(ds.miles / cs.energyKWh).toFixed(2)} mi/kWh`
                  : ds.milesPerPct > 0
                    ? `${ds.milesPerPct.toFixed(2)} mi/%`
                    : "—"
              }
              hint={
                cs.energyKWh > 0 && ds.miles > 0
                  ? `${num(ds.miles, 0, "mi")} / ${num(cs.energyKWh, 0, "kWh")}`
                  : `top speed ${num(ds.maxSpeedMph, 0, "mph")}`
              }
            />
          </div>

          <Card title={`Miles per day · last ${barDays}`}>
            <BarChart
              data={dailyMiles}
              height={140}
              formatY={(v) => `${v.toFixed(0)}`}
              formatX={(label) => label.slice(5)}
            />
          </Card>

          <Card title="Battery (SoC) trend">
            <LineChart
              series={[
                {
                  points: trend,
                  color: "#10b981",
                  area: true,
                  strokeWidth: 1.2,
                },
              ]}
              height={160}
              yDomain={[0, 100]}
              formatY={(v) => `${v.toFixed(0)}%`}
              formatX={(x) =>
                new Date(x).toLocaleDateString(undefined, {
                  month: "short",
                  day: "numeric",
                })
              }
            />
          </Card>

          <div className="grid md:grid-cols-2 gap-6">
            <Card
              title="Recent drives"
              actions={
                <Link to="/drives" className="text-xs text-emerald-400 hover:underline">
                  all drives →
                </Link>
              }
            >
              {winDrives.length === 0 ? (
                <EmptyState kind="drives in window" />
              ) : (
                <ul className="divide-y divide-neutral-800">
                  {winDrives.slice(0, 6).map((d) => (
                    <li key={d.ID}>
                      <Link
                        to={`/drives/${d.ID}`}
                        className="-mx-1 flex items-center justify-between rounded px-1 py-2 text-sm hover:bg-neutral-800/40"
                      >
                        <span className="text-neutral-300">
                          {formatDateTime(d.StartedAt)}
                        </span>
                        <span className="text-neutral-400 tabular-nums">
                          {num(d.DistanceMi, 1, "mi")} ·{" "}
                          {pct(d.StartSoCPct)}→{pct(d.EndSoCPct)}
                        </span>
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
            </Card>

            <Card
              title="Recent charges"
              actions={
                <Link to="/charges" className="text-xs text-emerald-400 hover:underline">
                  all charges →
                </Link>
              }
            >
              {winCharges.length === 0 ? (
                <EmptyState kind="charges in window" />
              ) : (
                <ul className="divide-y divide-neutral-800">
                  {winCharges.slice(0, 6).map((c) => (
                    <li key={c.ID}>
                      <Link
                        to={`/charges/${c.ID}`}
                        className="-mx-1 flex items-center justify-between rounded px-1 py-2 text-sm hover:bg-neutral-800/40"
                      >
                        <span className="text-neutral-300">
                          {formatDateTime(c.StartedAt)}
                        </span>
                        <span className="text-neutral-400 tabular-nums">
                          {formatDuration(durationSeconds(c.StartedAt, c.EndedAt))} ·{" "}
                          {pct(c.StartSoCPct)}→{pct(c.EndSoCPct)} ·{" "}
                          {num(c.EnergyAddedKWh, 1, "kWh")}
                        </span>
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          </div>
        </>
      )}
    </div>
  );
}

function Stat({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-3">
      <div className="text-xs text-neutral-500">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums">{value}</div>
      {hint ? (
        <div className="mt-1 text-[11px] text-neutral-500 tabular-nums">{hint}</div>
      ) : null}
    </div>
  );
}

function EmptyState({ kind }: { kind: string }) {
  return <p className="text-sm text-neutral-500">No {kind}.</p>;
}

// HeroBanner is the Overview's marketing frame AND filter anchor.
// Top row = tagline pill / headline / description / CTAs (kept
// intact from v0.3.34's Caffeine-style hero). Bottom row = a thin
// divider + "Showing" label + WindowPicker so everything below the
// hero inherits the picker's scope visually. Folding the picker
// into the hero replaces the previous orphan 'Summary · 30 days'
// header row that floated between two bordered cards.
function HeroBanner({
  win,
  onWinChange,
}: {
  win: WindowKey;
  onWinChange: (w: WindowKey) => void;
}) {
  const currentLabel =
    WINDOW_OPTIONS.find((o) => o.key === win)?.label ?? "";
  return (
    <section className="relative overflow-hidden rounded-xl border border-neutral-800 bg-gradient-to-br from-neutral-900 via-neutral-950 to-neutral-900">
      <svg
        aria-hidden="true"
        viewBox="0 0 24 24"
        className="pointer-events-none absolute -right-2 top-1/2 hidden h-32 w-32 -translate-y-1/2 text-emerald-500/10 md:block"
        fill="currentColor"
      >
        <path d="M13 2 4 14h6l-1 8 9-12h-6l1-8z" />
      </svg>
      <div className="relative flex flex-col gap-3 px-4 py-4 md:flex-row md:items-center md:justify-between">
        <div className="min-w-0 max-w-2xl">
          <span className="inline-flex items-center gap-1.5 rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-0.5 text-[10px] uppercase tracking-wide text-emerald-300">
            <span className="h-1.5 w-1.5 rounded-full bg-emerald-400" />
            Your Rivian, your data
          </span>
          <h1 className="mt-1.5 text-lg font-semibold tracking-tight sm:text-xl">
            <span className="text-neutral-100">Drive more.</span>{" "}
            <span className="text-emerald-300">Know it better.</span>
          </h1>
          <p className="mt-1 text-[12px] text-neutral-400 sm:text-sm">
            Live telemetry, full drive &amp; charge history, session-level cost tracking. Runs on your network.
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-3">
          <Link
            to="/live"
            className="inline-flex items-center gap-1 rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white shadow hover:bg-emerald-500"
          >
            Live view →
          </Link>
          <Link
            to="/drives"
            className="text-sm text-neutral-400 hover:text-neutral-200"
          >
            Browse history
          </Link>
        </div>
      </div>
      {/* Filter footer: hairline divider + scope label + picker. The
          label uses aria-hidden text because WindowPicker already
          labels itself; we just want a visual cue that the picker
          governs the summary below. currentLabel appears on sm+ to
          avoid wrapping on narrow screens where the picker is wide. */}
      <div className="relative flex flex-wrap items-center justify-between gap-2 border-t border-neutral-800/80 bg-neutral-950/40 px-4 py-2">
        <div className="text-[11px] uppercase tracking-wide text-neutral-500">
          Showing{" "}
          <span className="hidden text-neutral-300 sm:inline">
            · {currentLabel}
          </span>
        </div>
        <WindowPicker value={win} onChange={onWinChange} />
      </div>
    </section>
  );
}
