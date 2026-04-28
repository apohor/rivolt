import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { backend, type Sample } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { LineChart } from "../components/charts";
import { DriveMap } from "../components/DriveMap";
import {
  durationSeconds,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";
import { smoothGaussianTime } from "../lib/smooth";

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
  // exactly at the first sample edge, and — critically — so we catch
  // the parked samples before and after the drive. The stored
  // Drive.Start/EndLat (and the first/last in-drive GPS sample) can
  // miss the true start/end by up to a mile because telemetry often
  // drops the first 60–90 seconds of a trip: the first sample arrives
  // with the car already at highway speed, far from home.
  const samples = useQuery({
    queryKey: ["samples", "drive", id],
    enabled: !!drive,
    queryFn: () => {
      const since = new Date(
        new Date(drive!.StartedAt).getTime() - 10 * 60_000,
      );
      return backend.samples(since, 20_000);
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

  // Infer "home" endpoints from the last parked sample before the drive
  // and the first parked sample after it. These are far more reliable
  // than the drive's stored Start/EndLat, which come from whenever the
  // first mid-drive telemetry packet happened to arrive.
  const homeStart = useMemo(() => {
    if (!drive || !samples.data) return undefined;
    const ts = new Date(drive.StartedAt).getTime();
    const windowStart = ts - 10 * 60_000;
    const parked = samples.data
      .filter((p) => {
        const t = new Date(p.At).getTime();
        return (
          t >= windowStart &&
          t < ts &&
          p.ShiftState === "P" &&
          (p.Lat !== 0 || p.Lon !== 0)
        );
      })
      .sort(
        (a, b) => new Date(a.At).getTime() - new Date(b.At).getTime(),
      );
    const last = parked[parked.length - 1];
    return last ? { lat: last.Lat, lon: last.Lon } : undefined;
  }, [drive, samples.data]);

  const homeEnd = useMemo(() => {
    if (!drive || !samples.data) return undefined;
    const te = new Date(drive.EndedAt).getTime();
    const windowEnd = te + 10 * 60_000;
    const parked = samples.data
      .filter((p) => {
        const t = new Date(p.At).getTime();
        return (
          t > te &&
          t <= windowEnd &&
          p.ShiftState === "P" &&
          (p.Lat !== 0 || p.Lon !== 0)
        );
      })
      .sort(
        (a, b) => new Date(a.At).getTime() - new Date(b.At).getTime(),
      );
    const first = parked[0];
    return first ? { lat: first.Lat, lon: first.Lon } : undefined;
  }, [drive, samples.data]);

  // Samples to feed the route map. Same window as `driveSamples`,
  // but with two extra constraints that matter for OSRM /match:
  //
  //   1. Hard-cap at EndedAt (no post-end pad). The 60 s pad on
  //      driveSamples exists so the speed chart can visibly return
  //      to 0, but on back-to-back trips (e.g. driver pauses < 60 s
  //      between drives) it bleeds samples from the *next* drive
  //      onto this drive's polyline. Those bleed samples are
  //      `ShiftState === "D"` so the parked-frame trim below
  //      doesn't catch them — only the time cap does.
  //
  //   2. Strip leading/trailing `ShiftState === "P"` frames. When
  //      Rivian transitions D → P at the destination, telemetry
  //      often replays the last in-motion sample several times
  //      (same lat/lon, frozen non-zero speed). /match treats
  //      those as a slow crawl and snaps each onto whichever
  //      local street is nearest, producing a phantom loop next
  //      to the end pin.
  //
  // Charts still consume the full driveSamples window so the
  // speed return-to-zero animation is preserved.
  const mapPathSamples = useMemo(() => {
    if (!drive) return [] as Sample[];
    const endMs = new Date(drive.EndedAt).getTime();
    const inWindow = driveSamples.filter(
      (p) => new Date(p.At).getTime() <= endMs,
    );
    let head = 0;
    while (head < inWindow.length && inWindow[head].ShiftState === "P") head++;
    let tail = inWindow.length;
    while (tail > head && inWindow[tail - 1].ShiftState === "P") tail--;
    return inWindow.slice(head, tail);
  }, [drive, driveSamples]);

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

  const speedPtsRaw = driveSamples.map((p) => ({
    x: new Date(p.At).getTime(),
    y: p.SpeedMph || 0,
  }));
  const socPtsRaw = driveSamples.map((p) => ({
    x: new Date(p.At).getTime(),
    y: p.BatteryLevelPct || 0,
  }));
  const speedPts = speedPtsRaw;
  const socPts = smoothGaussianTime(socPtsRaw, 30_000);
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
        <Stat
          label="Energy"
          value={drive.EnergyUsedKWh > 0 ? num(drive.EnergyUsedKWh, 1, "kWh") : "—"}
        />
        <Stat
          label="Cost"
          value={
            drive.estimated_cost && drive.estimated_cost > 0
              ? `~${drive.estimated_cost.toFixed(2)}${drive.estimated_currency ? ` ${drive.estimated_currency}` : ""}`
              : "—"
          }
          hint={
            drive.estimated_price_per_kwh
              ? `at ~${drive.estimated_price_per_kwh.toFixed(3)}${drive.estimated_currency ? ` ${drive.estimated_currency}` : ""}/kWh from your most recent charge`
              : undefined
          }
        />
        <Stat
          label="Efficiency"
          value={
            drive.EnergyUsedKWh > 0 && drive.DistanceMi > 0
              ? `${(drive.DistanceMi / drive.EnergyUsedKWh).toFixed(2)} mi/kWh`
              : "—"
          }
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
                strokeWidth: 1.4,
                area: true,
                curve: "monotone",
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

      <Card title="Route">
        {samples.isLoading ? (
          <Spinner />
        ) : mapPathSamples.length === 0 ? (
          <NoSamples />
        ) : (
          <DriveMap
            points={mapPathSamples.map((p) => ({
              lat: p.Lat,
              lon: p.Lon,
              // Unix seconds — OSRM /match needs a monotonic time
              // axis to weight kinematic plausibility against
              // each candidate road.
              t: Math.floor(new Date(p.At).getTime() / 1000),
            }))}
            start={homeStart ?? { lat: drive.StartLat, lon: drive.StartLon }}
            end={homeEnd ?? { lat: drive.EndLat, lon: drive.EndLon }}
            height={360}
          />
        )}
      </Card>

      <Card title="Endpoints">
        <div className="grid grid-cols-2 gap-4 text-sm text-neutral-300">
          <Endpoint label="Start" lat={drive.StartLat} lon={drive.StartLon} />
          <Endpoint label="End" lat={drive.EndLat} lon={drive.EndLon} />
        </div>
        {hasEndpointPair(drive) ? (
          <div className="mt-4 border-t border-neutral-800 pt-3">
            <a
              href={googleRouteURL(drive)}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 rounded-md border border-emerald-700/60 bg-emerald-900/30 px-3 py-1.5 text-xs font-medium text-emerald-300 hover:bg-emerald-900/50 hover:text-emerald-200"
            >
              Open route in Google Maps
              <span aria-hidden>↗</span>
            </a>
          </div>
        ) : null}
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

// Google Maps directions URL with driving mode, origin, and destination.
// We omit waypoints — we have hundreds of samples per drive and the URL
// has a 10-waypoint cap anyway. Google routes between endpoints fine;
// this is "navigate me home" UX, not a polyline replayer.
function googleRouteURL(d: {
  StartLat: number;
  StartLon: number;
  EndLat: number;
  EndLon: number;
}): string {
  const origin = `${d.StartLat},${d.StartLon}`;
  const dest = `${d.EndLat},${d.EndLon}`;
  return `https://www.google.com/maps/dir/?api=1&origin=${origin}&destination=${dest}&travelmode=driving`;
}

function hasEndpointPair(d: {
  StartLat: number;
  StartLon: number;
  EndLat: number;
  EndLon: number;
}): boolean {
  const ok = (lat: number, lon: number) =>
    Number.isFinite(lat) && Number.isFinite(lon) && (lat !== 0 || lon !== 0);
  return ok(d.StartLat, d.StartLon) && ok(d.EndLat, d.EndLon);
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
      <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
      {hint ? (
        <div className="mt-1 text-[10px] text-neutral-500">{hint}</div>
      ) : null}
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
