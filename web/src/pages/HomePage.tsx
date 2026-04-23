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
      {/* The nav already shows the page name in its highlighted state,
          so a redundant h1 just eats vertical space above the fold.
          Keep only the WindowPicker right-aligned. */}
      <div className="flex justify-end">
        <WindowPicker value={win} onChange={setWin} />
      </div>

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
