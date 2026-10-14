import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { BarChart } from "../components/charts";
import { PackHealthCard } from "../components/PackHealthCard";
import { VehiclePicker } from "../components/VehiclePicker";
import { Card, PageHeader } from "../components/ui";
import { useSelectedVehicle } from "../lib/selectedVehicle";
import { lowestTire, sleepSummary, softwareInfo } from "../lib/truckHealth";

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
    enabled: !!rivianStatus.data?.authenticated,
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
  const since = useMemo(() => new Date(Date.now() - 30 * 86_400_000).toISOString(), []);
  const sleepQ = useQuery({
    queryKey: ["sleepActivity", "truck", selectedID ?? ""],
    queryFn: () => backend.sleepActivity(since, selectedID ?? undefined),
    enabled: !!selectedID,
    staleTime: 5 * 60_000,
    retry: 1,
  });

  const sleep = useMemo(() => sleepSummary(sleepQ.data ?? []), [sleepQ.data]);
  const s = stateQ.data;
  const tire = s ? lowestTire(s) : null;
  const software = s ? softwareInfo(s) : null;
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
    <div className="space-y-4">
      <PageHeader
        title="Truck"
        subtitle={name}
        actions={<VehiclePicker vehicles={vehicles.data ?? []} selectedID={selectedID} onChange={select} />}
      />

      <Card title="Pack capacity">
        <PackHealthCard vehicleID={selectedID} />
      </Card>

      <Card title="Sleep and idle-awake">
        {sleep.trouble && (
          <p role="status" className="mb-3 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-100/90">
            {name} woke up again within a minute {sleep.trouble.failed} of {sleep.trouble.sleeps} times in the last two days.
          </p>
        )}
        {sleep.daysWithData > 0 ? (
          <>
            <BarChart
              data={sleep.bars}
              height={140}
              color="#6366f1"
              formatY={(v) => `${v.toFixed(0)}m`}
              formatX={(label) => String(label).slice(5)}
            />
            <p className="mt-2 text-[11px] text-neutral-500">
              Minutes per day awake while parked and not charging
              {sleep.avgDays > 0 && (
                <>, averaging {Math.round(sleep.avgIdleMinutes)} min over the last {sleep.avgDays} full days</>
              )}
              . Today is still in progress. Driving and charging are excluded.
            </p>
          </>
        ) : (
          <p className="text-xs text-neutral-500">
            No sleep data yet. This fills in as power-state recording builds up history.
          </p>
        )}
      </Card>

      {(tire || software) && (
        <Card title="Vehicle">
          <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
            {tire && (
              <>
                <dt className="text-neutral-500">Lowest tire</dt>
                <dd className={`text-right tabular-nums ${tire.low ? "text-amber-300" : "text-neutral-200"}`}>
                  {tire.bar.toFixed(1)} bar · {tire.psi.toFixed(0)} psi
                </dd>
              </>
            )}
            {software && (
              <>
                <dt className="text-neutral-500">Software</dt>
                <dd className="text-right tabular-nums text-neutral-200">{software.current}</dd>
                {software.available && (
                  <>
                    <dt className="text-neutral-500">Update available</dt>
                    <dd className="text-right tabular-nums text-neutral-200">{software.available}</dd>
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
