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

export default function DriveDetailPage() {
  const { id } = useParams<{ id: string }>();
  const drives = useQuery({
    queryKey: ["drives", "all"],
    queryFn: () => backend.allDrives(),
  });

  const drive = useMemo(
    () => drives.data?.find((d) => d.ID === id),
    [drives.data, id],
  );

  // Pull a bit of padding around the drive so chart doesn't start
  // exactly at the first sample edge.
  const samples = useQuery({
    queryKey: ["samples", "drive", id],
    enabled: !!drive,
    queryFn: () => {
      const since = new Date(
        new Date(drive!.StartedAt).getTime() - 60_000,
      );
      return backend.samples(since, 10_000);
    },
  });

  const driveSamples = useMemo(() => {
    if (!drive || !samples.data) return [] as Sample[];
    const s = new Date(drive.StartedAt).getTime();
    const e = new Date(drive.EndedAt).getTime() + 60_000;
    return samples.data.filter((p) => {
      const t = new Date(p.At).getTime();
      return t >= s && t <= e;
    });
  }, [drive, samples.data]);

  if (drives.isLoading) {
    return (
      <div>
        <PageHeader title="Drive" />
        <Spinner />
      </div>
    );
  }
  if (!drive) {
    return (
      <div>
        <PageHeader title="Drive not found" />
        <Card>
          <p className="text-sm text-neutral-400">
            That drive ID doesn't exist in this dataset.{" "}
            <Link to="/drives" className="text-emerald-400 hover:underline">
              Back to drives →
            </Link>
          </p>
        </Card>
      </div>
    );
  }

  const speedPts = driveSamples.map((p) => ({
    x: new Date(p.At).getTime(),
    y: p.SpeedMph || 0,
  }));
  const socPts = driveSamples.map((p) => ({
    x: new Date(p.At).getTime(),
    y: p.BatteryLevelPct || 0,
  }));
  const duration = durationSeconds(drive.StartedAt, drive.EndedAt);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Drive"
        subtitle={`${formatDateTime(drive.StartedAt)} → ${formatDateTime(drive.EndedAt)}`}
        actions={
          <Link
            to="/drives"
            className="text-xs text-neutral-400 hover:text-neutral-200"
          >
            ← all drives
          </Link>
        }
      />

      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <Stat label="Distance" value={num(drive.DistanceMi, 1, "mi")} />
        <Stat label="Duration" value={formatDuration(duration)} />
        <Stat
          label="SoC"
          value={`${pct(drive.StartSoCPct)} → ${pct(drive.EndSoCPct)}`}
        />
        <Stat
          label="Speed avg / max"
          value={`${num(drive.AvgSpeedMph, 0)} / ${num(drive.MaxSpeedMph, 0)} mph`}
        />
      </div>

      <Card title="Speed">
        {samples.isLoading ? (
          <Spinner />
        ) : speedPts.length === 0 ? (
          <NoSamples />
        ) : (
          <LineChart
            series={[
              {
                points: speedPts,
                color: "#38bdf8",
                strokeWidth: 1.2,
                area: true,
              },
            ]}
            height={180}
            formatY={(v) => `${v.toFixed(0)} mph`}
            formatX={xTimeFmt}
          />
        )}
      </Card>

      <Card title="Battery">
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
              },
            ]}
            height={140}
            yDomain={[
              Math.max(0, drive.EndSoCPct - 5),
              Math.min(100, drive.StartSoCPct + 5),
            ]}
            formatY={(v) => `${v.toFixed(0)}%`}
            formatX={xTimeFmt}
          />
        )}
      </Card>

      <Card title="Endpoints">
        <div className="grid grid-cols-2 gap-4 text-sm text-neutral-300">
          <Endpoint label="Start" lat={drive.StartLat} lon={drive.StartLon} />
          <Endpoint label="End" lat={drive.EndLat} lon={drive.EndLon} />
        </div>
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

function Endpoint({ label, lat, lon }: { label: string; lat: number; lon: number }) {
  const hasCoords = Number.isFinite(lat) && Number.isFinite(lon) && (lat !== 0 || lon !== 0);
  const href = hasCoords
    ? `https://www.google.com/maps/search/?api=1&query=${lat},${lon}`
    : undefined;
  return (
    <div>
      <div className="text-xs uppercase tracking-wide text-neutral-500">{label}</div>
      {hasCoords ? (
        <a
          href={href}
          target="_blank"
          rel="noreferrer"
          className="mt-1 inline-block font-mono text-emerald-400 hover:underline"
        >
          {lat.toFixed(4)}, {lon.toFixed(4)}
        </a>
      ) : (
        <div className="mt-1 text-neutral-500">—</div>
      )}
    </div>
  );
}

function NoSamples() {
  return (
    <p className="text-sm text-neutral-500">
      No raw samples stored for this time window. Live ingestion isn't
      wired yet; ElectraFi samples only land in the <code>samples</code>{" "}
      table for runs after the importer was added.
    </p>
  );
}
