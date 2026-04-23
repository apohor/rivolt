import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { backend } from "../lib/api";
import { Card, Spinner, ErrorBox } from "../components/ui";
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
  type WindowKey,
} from "../lib/analytics";
import { WindowPicker } from "../components/WindowPicker";
import { LiveSummary } from "../components/LiveSummary";

export default function HomePage() {
  const [win, setWin] = useState<WindowKey>("30d");

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
  const winDrives = useMemo(() => filterByWindow(all, win), [all, win]);
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

  const isLoading = drives.isLoading || charges.isLoading;
  const isError = drives.isError || charges.isError;

  return (
    <div className="space-y-6">
      <HeroBanner />

      {/* The nav already shows the page name in its highlighted state,
          so a redundant h1 just eats vertical space above the fold.
          Keep only the WindowPicker right-aligned. */}
      <div className="flex justify-end">
        <WindowPicker value={win} onChange={setWin} />
      </div>

      {/* Slim glanceable strip: current SoC + remaining range in a
          single row. Always visible (even before the Live card
          resolves) so the most important numbers never sit below
          the fold. */}
      {liveSoC > 0 || liveState.data ? (
        <div className="flex items-baseline justify-between rounded-xl border border-neutral-800 bg-neutral-900/50 px-4 py-3">
          <div className="flex items-baseline gap-4 tabular-nums">
            <span className="text-3xl font-semibold text-emerald-300">
              {pct(liveSoC, 0)}
            </span>
            <span className="text-xl text-neutral-300">
              {num(
                (liveState.data?.distance_to_empty ?? 0) * 0.6213711922,
                0,
                "mi",
              )}
            </span>
          </div>
          <span className="text-[11px] uppercase tracking-wide text-neutral-500">
            battery · range
          </span>
        </div>
      ) : null}

      <LiveSummary />

      {isLoading ? (
        <Spinner />
      ) : isError ? (
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

// HeroBanner is the marketing-style top frame for the Overview — a
// tagline pill, two-line headline, short product description, and a
// CTA into the live view. Matches the visual structure of Caffeine's
// home hero so the two self-hosted apps feel like a family. The
// decorative lightning glyph floats in the right third of the banner
// and is purely ornamental.
function HeroBanner() {
  return (
    <section className="relative overflow-hidden rounded-2xl border border-neutral-800 bg-gradient-to-br from-neutral-900 via-neutral-950 to-neutral-900 px-6 py-8 sm:px-8 sm:py-10">
      {/* Decorative lightning bolt — emerald, heavily faded. Sits
          absolutely in the right half and is cropped on narrow
          viewports so it never competes with the headline. */}
      <svg
        aria-hidden="true"
        viewBox="0 0 24 24"
        className="pointer-events-none absolute -right-4 top-1/2 hidden h-48 w-48 -translate-y-1/2 text-emerald-500/10 md:block"
        fill="currentColor"
      >
        <path d="M13 2 4 14h6l-1 8 9-12h-6l1-8z" />
      </svg>
      <div className="relative max-w-2xl">
        <span className="inline-flex items-center gap-1.5 rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2.5 py-0.5 text-xs text-emerald-300">
          <span className="h-1.5 w-1.5 rounded-full bg-emerald-400" />
          Your Rivian, your data
        </span>
        <h1 className="mt-4 text-3xl font-bold tracking-tight sm:text-4xl">
          <span className="block text-neutral-100">Drive more.</span>
          <span className="block text-emerald-300">Know it better.</span>
        </h1>
        <p className="mt-3 max-w-xl text-sm text-neutral-400 sm:text-base">
          Live telemetry from your Rivian, a full history of drives and
          charges with charts, and session-level cost tracking for home
          and public charging. Runs on your network — nothing leaves
          the box.
        </p>
        <div className="mt-5 flex items-center gap-3">
          <Link
            to="/live"
            className="inline-flex items-center gap-1.5 rounded-md bg-emerald-600 px-3.5 py-2 text-sm font-medium text-white shadow hover:bg-emerald-500"
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
    </section>
  );
}
