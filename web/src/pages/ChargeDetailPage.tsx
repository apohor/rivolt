import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { backend, type Sample } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { LineChart } from "../components/charts";
import {
  durationSeconds,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";

export default function ChargeDetailPage() {
  const { id } = useParams<{ id: string }>();
  const charges = useQuery({
    queryKey: ["charges", "all"],
    queryFn: () => backend.allCharges(),
  });

  const charge = useMemo(
    () => charges.data?.find((c) => c.ID === id),
    [charges.data, id],
  );

  const samples = useQuery({
    queryKey: ["samples", "charge", id],
    enabled: !!charge,
    queryFn: () => {
      const since = new Date(
        new Date(charge!.StartedAt).getTime() - 60_000,
      );
      return backend.samples(since, 10_000);
    },
  });

  const chargeSamples = useMemo(() => {
    if (!charge || !samples.data) return [] as Sample[];
    const s = new Date(charge.StartedAt).getTime();
    const e = new Date(charge.EndedAt).getTime() + 60_000;
    return samples.data.filter((p) => {
      const t = new Date(p.At).getTime();
      return t >= s && t <= e;
    });
  }, [charge, samples.data]);

  if (charges.isLoading) {
    return (
      <div>
        <PageHeader title="Charge" />
        <Spinner />
      </div>
    );
  }
  if (!charge) {
    return (
      <div>
        <PageHeader title="Charge not found" />
        <Card>
          <p className="text-sm text-neutral-400">
            That charge ID doesn't exist in this dataset.{" "}
            <Link to="/charges" className="text-emerald-400 hover:underline">
              Back to charges →
            </Link>
          </p>
        </Card>
      </div>
    );
  }

  const socPts = chargeSamples.map((p) => ({
    x: new Date(p.At).getTime(),
    y: p.BatteryLevelPct || 0,
  }));
  const powerPts = chargeSamples
    .filter((p) => p.ChargerPowerKW > 0)
    .map((p) => ({
      x: new Date(p.At).getTime(),
      y: p.ChargerPowerKW,
    }));
  const duration = durationSeconds(charge.StartedAt, charge.EndedAt);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Charge"
        subtitle={`${formatDateTime(charge.StartedAt)} → ${formatDateTime(charge.EndedAt)}`}
        actions={
          <Link
            to="/charges"
            className="text-xs text-neutral-400 hover:text-neutral-200"
          >
            ← all charges
          </Link>
        }
      />

      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <Stat label="Duration" value={formatDuration(duration)} />
        <Stat
          label="SoC"
          value={`${pct(charge.StartSoCPct)} → ${pct(charge.EndSoCPct)}`}
        />
        <Stat label="Energy" value={num(charge.EnergyAddedKWh, 1, "kWh")} />
        <Stat label="Max kW" value={num(charge.MaxPowerKW, 1, "kW")} />
      </div>

      <Card title="Battery state">
        {samples.isLoading ? (
          <Spinner />
        ) : socPts.length === 0 ? (
          <NoSamples />
        ) : (
          <LineChart
            series={[
              {
                points: socPts,
                color: "#10b981",
                strokeWidth: 1.4,
                area: true,
              },
            ]}
            height={180}
            yDomain={[
              Math.max(0, charge.StartSoCPct - 5),
              Math.min(100, charge.EndSoCPct + 5),
            ]}
            formatY={(v) => `${v.toFixed(0)}%`}
            formatX={xTimeFmt}
          />
        )}
      </Card>

      <Card title="Charger power">
        {samples.isLoading ? (
          <Spinner />
        ) : powerPts.length === 0 ? (
          <p className="text-sm text-neutral-500">
            No charger power samples recorded (the ElectraFi export
            stopped reporting <code>charger_power</code> for Rivians in
            Mar 2026). Energy and peak power are still reconstructed
            from SoC deltas at the session level.
          </p>
        ) : (
          <LineChart
            series={[
              {
                points: powerPts,
                color: "#f59e0b",
                strokeWidth: 1.2,
                area: true,
              },
            ]}
            height={160}
            formatY={(v) => `${v.toFixed(0)} kW`}
            formatX={xTimeFmt}
          />
        )}
      </Card>

      <Card title="Session">
        <dl className="grid grid-cols-2 gap-3 text-sm text-neutral-300">
          <Row label="Final state" value={charge.FinalState || "—"} />
          <Row
            label="Avg power"
            value={num(charge.AvgPowerKW, 1, "kW")}
          />
          <Row
            label="Miles added"
            value={num(charge.MilesAdded, 1, "mi")}
          />
          <Row label="Source" value={charge.Source} />
        </dl>
      </Card>

      {samples.isError ? (
        <ErrorBox
          title="Sample data unavailable"
          detail={String(samples.error)}
        />
      ) : null}
    </div>
  );
}

function xTimeFmt(x: number): string {
  return new Date(x).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  });
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-3">
      <div className="text-xs text-neutral-500">{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-neutral-500">{label}</dt>
      <dd className="mt-1 tabular-nums">{value}</dd>
    </div>
  );
}

function NoSamples() {
  return (
    <p className="text-sm text-neutral-500">
      No raw samples stored for this time window.
    </p>
  );
}
