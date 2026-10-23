import { useEffect, useMemo } from "react";
import { PeriodPicker } from "../components/PeriodPicker";
import { periodRange, sleepWindow, usePeriod } from "../lib/period";
import { formatMinutes, formatMinutesShort } from "../lib/minutes";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { inDemo } from "../lib/demo";
import { BarChart } from "../components/charts";
import { BatteryCapacityCard } from "../components/BatteryCapacityCard";
import { TrendsCard } from "../components/TrendsCard";
import { ColdRangeCard } from "../components/ColdRangeCard";
import DrainCard from "../components/DrainCard";
import { TruckSummary } from "../components/TruckSummary";
import { VehiclePicker } from "../components/VehiclePicker";
import { LeaseCard } from "../components/LeaseCard";
import { R2CheckedCard, isR2 } from "../components/R2Checked";
import { Card, PageHeader } from "../components/ui";
import { useSelectedVehicle } from "../lib/selectedVehicle";
import { otaLine, otaUpdate, sleepSummary, softwareInfo, tireWheels, type Wheel } from "../lib/truckHealth";
import { formatAgo } from "../lib/nowState";

// Truck tab v1 (ux-report S4): pack capacity, sleep and idle-awake,
// tires, software version. Gated by money_tab_enabled. Fields the
// vehicle never reports are hidden, not shown as "—" (NQ15). No
// sleep-safe badge: the sleep gate is off in prod.

export default function TruckPage() {
  const rivianStatus = useQuery({
    queryKey: ["rivian", "status"],
    queryFn: () => backend.rivianStatus(),
    staleTime: 30_000,
    retry: 1,
  });
  const vehicles = useQuery({
    queryKey: ["rivian", "vehicles"],
    queryFn: () => backend.vehicles(),
    // The signed-out demo has no Rivian login; its sample truck is listed anyway.
    enabled: !!rivianStatus.data?.authenticated || inDemo(),
    staleTime: 5 * 60_000,
    retry: 1,
  });
  const { selectedID, select } = useSelectedVehicle(vehicles.data);
  const vehicle = vehicles.data?.find((v) => v.id === selectedID);

  const stateQ = useQuery({
    queryKey: ["rivian", "state", selectedID ?? ""],
    queryFn: () => backend.vehicleState(selectedID as string),
    enabled: !!selectedID,
    refetchInterval: 60_000,
    retry: 1,
  });
  // The shared calendar scope drives the sleep chart.
  const [period, setPeriod] = usePeriod();
  const range = useMemo(() => periodRange(period), [period]);
  const win = useMemo(() => sleepWindow(range), [range]);
  const since = useMemo(
    () => win?.since ?? new Date(Date.now() - 30 * 86_400_000).toISOString(),
    [win],
  );
  const until = win?.until;
  const sleepQ = useQuery({
    queryKey: ["sleepActivity", "truck", selectedID ?? "", since, until ?? ""],
    queryFn: () => backend.sleepActivity(since, selectedID ?? undefined, until),
    enabled: !!selectedID,
    staleTime: 5 * 60_000,
    retry: 1,
  });

  const sleep = useMemo(() => sleepSummary(sleepQ.data ?? []), [sleepQ.data]);
  // Deep link from Now's idle-awake tile (#sleep): scroll once the card exists.
  useEffect(() => {
    if (window.location.hash === "#sleep" && selectedID) {
      document.getElementById("sleep")?.scrollIntoView({ block: "start" });
    }
  }, [selectedID, sleepQ.isLoading]);
  const s = stateQ.data;
  const wheels = s ? tireWheels(s) : null;
  const software = s ? softwareInfo(s) : null;
  const update = s ? otaUpdate(s) : null;
  const updateAgeSec = s ? Math.max(0, (Date.now() - Date.parse(s.at)) / 1000) : 0;
  const name = vehicle?.name || "Your truck";

  if (!selectedID) {
    return (
      <div className="space-y-4">
        <PageHeader title="Truck" subtitle="Health and sleep" />
        <Card>
          <p className="text-sm text-neutral-400">Connect your Rivian to see pack health and sleep here.</p>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-4" data-ui-grid="truck">
      <PageHeader
        title="Truck"
        subtitle={name}
        actions={
          <>
            <PeriodPicker value={period} onChange={setPeriod} />
            <VehiclePicker vehicles={vehicles.data ?? []} selectedID={selectedID} onChange={select} />
          </>
        }
      />

      {/* The first screen answers "how is the truck": the two figures from the cards below. */}
      <TruckSummary vehicleID={selectedID} />

      {isR2(vehicle?.model) && <R2CheckedCard />}

      <DrainCard vehicleID={selectedID} />

      {/* The demo has no lease (and its API has no such call): no card, no 404 in the console. */}
      {!inDemo() && <LeaseCard vehicleID={selectedID} />}

      <BatteryCapacityCard vehicleID={selectedID} />

      {selectedID && <TrendsCard vehicleID={selectedID} />}

      {selectedID && <ColdRangeCard vehicleID={selectedID} />}

      <Card id="sleep" title="Sleep and idle-awake" span="full">
        {sleep.trouble && (
          <p role="status" className="mb-3 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-100/90">
            {name} woke up again within a minute {sleep.trouble.failed} of {sleep.trouble.sleeps} times in the last two days.
          </p>
        )}
        {sleep.daysWithData > 0 ? (
          <>
            <BarChart
              ariaLabel="Idle-awake time per day"
              data={sleep.bars}
              height={140}
              color="#6366f1"
              formatY={formatMinutesShort}
              formatValue={formatMinutes}
              formatX={(label) => String(label).slice(5)}
            />
            <p className="mt-2 text-[11px] text-neutral-500">
              Time per day awake while parked and not charging
              {sleep.avgDays > 0 && (
                <>, averaging {formatMinutes(sleep.avgIdleMinutes)} a day over the last {sleep.avgDays} full days</>
              )}
              . Today is still in progress. Driving and charging are excluded.
              {win?.capped && <> Showing the last 90 days of this period.</>}
            </p>
          </>
        ) : sleepQ.isLoading ? (
          <div className="h-24 animate-pulse rounded-sm bg-neutral-900/50" aria-label="Loading sleep data" />
        ) : sleepQ.isError ? (
          <p className="text-xs text-neutral-500">Couldn't load sleep data. Try again in a moment.</p>
        ) : (
          <p className="text-xs text-neutral-500">
            No sleep data yet. This fills in as power-state recording builds up history.
          </p>
        )}
      </Card>

      {wheels && s && <TiresCard wheels={wheels} at={s.at} />}

      {(software || update) && (
        <Card title="Vehicle" span="full">
          <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
            {update && (
              <>
                <dt className="text-neutral-500">Software update</dt>
                <dd data-ota-update className="text-right tabular-nums text-neutral-200">
                  {otaLine(update)}
                  {updateAgeSec > 120 && <span className="block text-xs text-neutral-500">As of {formatAgo(updateAgeSec)}</span>}
                </dd>
              </>
            )}
            {software && (
              <>
                <dt className="text-neutral-500">Software</dt>
                <dd className="text-right tabular-nums text-neutral-200">
                  {software.current}
                  <NotesLink vehicleID={vehicle?.id} which="current" has={vehicle?.ota?.current?.has_notes} />
                </dd>
                {software.available && (
                  <>
                    <dt className="text-neutral-500">Update available</dt>
                    <dd className="text-right tabular-nums text-neutral-200">
                      {software.available}
                      <NotesLink vehicleID={vehicle?.id} which="available" has={vehicle?.ota?.available?.has_notes} />
                    </dd>
                  </>
                )}
              </>
            )}
          </dl>
        </Card>
      )}
    </div>
  );
}

// NotesLink: "Release notes" for an update. Rivian's own link is a one-hour signed URL, so it is not stored or listed:
// this opens our endpoint, which asks Rivian for a fresh one when it is clicked and redirects there (new tab; the
// cookie carries the session). Never fetched by the page itself.
function NotesLink({ vehicleID, which, has }: { vehicleID?: string; which: "current" | "available"; has?: boolean }) {
  if (!has || !vehicleID) return null;
  const href = `/api/vehicles/${encodeURIComponent(vehicleID)}/ota/notes?which=${which}`;
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      data-ota-notes
      className="ml-2 whitespace-nowrap text-xs text-emerald-300 underline-offset-2 hover:underline"
    >
      Release notes<span aria-hidden> ↗</span>
    </a>
  );
}

// TiresCard: the four pressures from the truck's last reading, with its age (always: people act on a pressure). A wheel is marked low in
// plain amber text, no icon, below the fixed 32 psi floor or when Rivian's own status says so, and then it says whose word it is. Above
// the floor there is only the number: a placard pressure is far higher than the floor, and we never call a wheel OK in our own words.
function TiresCard({ wheels, at }: { wheels: Wheel[]; at: string }) {
  const ageSec = Math.max(0, (Date.now() - Date.parse(at)) / 1000);
  return (
    <Card title="Tires" span="full">
      <div data-tires className="space-y-3 text-sm">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
          {wheels.map((w) => (
            <div key={w.pos} data-tire={w.pos}>
              <dt className="text-xs text-neutral-500">{w.label}</dt>
              <dd className="tabular-nums text-neutral-100">
                {w.psi == null ? "—" : <>{w.psi.toFixed(0)} psi <span className="text-xs text-neutral-500">· {w.bar!.toFixed(1)} bar</span></>}
              </dd>
              {(w.low || w.rivianLow) && (
                <dd data-tire-low className="text-xs text-amber-300">
                  {w.low ? "Low" : "Low (Rivian)"}
                </dd>
              )}
            </div>
          ))}
        </dl>
        <p className="text-xs text-neutral-500" data-tires-age>
          As of {formatAgo(ageSec)}. Pressure changes with temperature. Your truck&apos;s pressure is on the placard on the driver&apos;s door.
          Marked low below 32 psi or when Rivian reports it.
        </p>
      </div>
    </Card>
  );
}
