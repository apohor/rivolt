import { useMemo, useState } from "react";
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
import { collapseRoundTrips } from "../lib/drives";
import { usePreferences, formatTemperature } from "../lib/preferences";

export default function DriveDetailPage() {
  const { id } = useParams<{ id: string }>();
  const prefs = usePreferences();
  // Shared cursor for the Speed chart, Battery chart and route map.
  // Stored in milliseconds so it can be passed straight through as
  // the chart x-axis cursor; converted to unix seconds for the map.
  const [cursorMs, setCursorMs] = useState<number | null>(null);
  const drives = useQuery({
    queryKey: ["drives", "all"],
    queryFn: () => backend.allDrives(),
  });

  // The drives list collapses A→B / B→A pairs into a single round-trip
  // row, but the row's link points at the first leg's ID. Apply the
  // same collapsing here so the detail page shows combined stats and
  // both legs of the route — otherwise the URL behind the row only
  // renders the first leg, contradicting the list. We also accept
  // the second leg's ID for completeness (a merged row is
  // addressable via either leg).
  const drive = useMemo(() => {
    if (!drives.data) return undefined;
    const direct = drives.data.find((d) => d.ID === id);
    if (!prefs.roundTripsEnabled) return direct;
    const collapsed = collapseRoundTrips(
      drives.data,
      prefs.roundTripRadiusMeters,
      prefs.roundTripMaxGapMinutes,
    );
    // The merged drive keeps the first leg's ID. Match by that, or
    // by the original drive's StartedAt falling within a merged
    // window (so navigating to the second leg also resolves).
    const byId = collapsed.find((d) => d.ID === id);
    if (byId) return byId;
    if (!direct) return undefined;
    const ds = new Date(direct.StartedAt).getTime();
    return collapsed.find((d) => {
      const s = new Date(d.StartedAt).getTime();
      const e = new Date(d.EndedAt).getTime();
      return ds >= s && ds <= e;
    });
  }, [
    drives.data,
    id,
    prefs.roundTripsEnabled,
    prefs.roundTripRadiusMeters,
    prefs.roundTripMaxGapMinutes,
  ]);

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
    const endMs = new Date(drive.EndedAt).getTime();
    // Pad 60 s past EndedAt so the speed chart can visibly return
    // to 0 instead of cutting off at the last in-motion sample.
    // BUT stop ingesting the moment we see a parked sample after
    // EndedAt — anything D-shift past that point belongs to the
    // *next* drive (Rivian's telemetry sometimes resumes within
    // 30–60 s of parking) and would pull the tail of this chart
    // back up to highway speed. The first P sample is exactly the
    // anchor we want for return-to-0; we keep it and drop the rest.
    const padEnd = endMs + 60_000;
    const out: Sample[] = [];
    let sawPostEndParked = false;
    for (const p of samples.data) {
      const t = new Date(p.At).getTime();
      if (t < s || t > padEnd) continue;
      if (t > endMs) {
        if (sawPostEndParked) break;
        if (p.ShiftState === "P") {
          out.push(p);
          sawPostEndParked = true;
          continue;
        }
      }
      out.push(p);
    }
    return out;
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

  // Stable map points: DriveMap's effect tears the map down whenever
  // its `points` array identity changes, so we MUST memoize the
  // mapped {lat,lon,t} list. Without this, every cursor-hover
  // re-render hands DriveMap a brand-new array, the map rebuilds
  // (zooming in/out visibly), and the cursor marker is wiped.
  const mapPoints = useMemo(
    () =>
      mapPathSamples.map((p) => ({
        lat: p.Lat,
        lon: p.Lon,
        // Unix seconds — OSRM /match needs a monotonic time axis,
        // and the cursor marker uses it to find the nearest sample.
        t: Math.floor(new Date(p.At).getTime() / 1000),
      })),
    [mapPathSamples],
  );

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

  // Temperature series. Convert to the user's chosen unit at the
  // points level so the chart Y-axis, formatY label and the cursor
  // readout all stay consistent (smoothing happens in chart units).
  // Filter out the (0, 0) sentinel samples emitted by the live merge
  // path when Rivian's WS feed didn't carry a fresh ambient reading
  // — a real 0 °C is rare and a phantom 0 line would distort the
  // y-domain. We accept any sample where at least one of the two
  // sensors reports a non-zero reading, then per-series we drop the
  // zero side (so e.g. live-only samples still contribute cabin).
  const tempUnit = prefs.temperatureUnit;
  const cToUnit = (c: number) => (tempUnit === "f" ? c * 1.8 + 32 : c);
  const tempUnitSuffix = tempUnit === "f" ? "°F" : "°C";
  const outsideTempPts = driveSamples
    .filter((p) => Number.isFinite(p.OutsideTempC) && p.OutsideTempC !== 0)
    .map((p) => ({
      x: new Date(p.At).getTime(),
      y: cToUnit(p.OutsideTempC),
    }));
  const insideTempPts = driveSamples
    .filter((p) => Number.isFinite(p.InsideTempC) && p.InsideTempC !== 0)
    .map((p) => ({
      x: new Date(p.At).getTime(),
      y: cToUnit(p.InsideTempC),
    }));
  // Outdoor temperature changes slowly (minutes, not seconds), so a
  // wide smoothing window cleans the typical ~1 °C sensor jitter
  // without flattening real ramps when driving in/out of sun.
  const outsideTempSmoothed = smoothGaussianTime(outsideTempPts, 60_000);
  const insideTempSmoothed = smoothGaussianTime(insideTempPts, 60_000);
  const hasTempSeries =
    outsideTempSmoothed.length > 0 || insideTempSmoothed.length > 0;

  // Resolve the sample closest to the synced cursor for the
  // time/speed/SoC/lat-lon readout. Uses the unsmoothed driveSamples
  // so the lat/lon stays exact (smoothing is a chart-only concern).
  const cursorSample = (() => {
    if (cursorMs == null || driveSamples.length === 0) return null;
    let best = driveSamples[0];
    let bestD = Math.abs(new Date(best.At).getTime() - cursorMs);
    for (let i = 1; i < driveSamples.length; i++) {
      const d = Math.abs(new Date(driveSamples[i].At).getTime() - cursorMs);
      if (d < bestD) {
        bestD = d;
        best = driveSamples[i];
      }
    }
    return best;
  })();

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

      {/* Synced cursor readout. Reserves a single line of vertical
          space whether or not the user is hovering, so adding/removing
          the cursor never shifts the charts below. */}
      <div className="h-5 -mt-2 text-xs font-mono text-neutral-300 flex items-center gap-3">
        {cursorSample ? (
          <>
            <span className="text-neutral-500">
              {new Date(cursorSample.At).toLocaleTimeString(undefined, {
                hour: "2-digit",
                minute: "2-digit",
                second: "2-digit",
              })}
            </span>
            <span className="text-sky-400">
              {(cursorSample.SpeedMph || 0).toFixed(0)} mph
            </span>
            <span className="text-emerald-400">
              {(cursorSample.BatteryLevelPct || 0).toFixed(0)}%
            </span>
            {cursorSample.OutsideTempC && cursorSample.OutsideTempC !== 0 ? (
              <span className="text-sky-300">
                {formatTemperature(cursorSample.OutsideTempC, tempUnit, 0)}
              </span>
            ) : null}
            {cursorSample.Lat || cursorSample.Lon ? (
              <span className="text-neutral-500">
                {cursorSample.Lat.toFixed(5)}, {cursorSample.Lon.toFixed(5)}
              </span>
            ) : null}
          </>
        ) : (
          <span className="text-neutral-600">
            Hover any chart or the route map to inspect a moment.
          </span>
        )}
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
            cursorX={cursorMs}
            onCursorChange={setCursorMs}
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
            cursorX={cursorMs}
            onCursorChange={setCursorMs}
          />
        )}
      </Card>

      {/* Temperature card. Only renders when we have at least one
          non-sentinel reading in the drive window — many drives
          predate the recorder writing temps, and live-only segments
          can carry only cabin or only outside. */}
      {samples.isLoading ? null : hasTempSeries ? (
        <Card title="Temperature">
          <LineChart
            series={[
              ...(outsideTempSmoothed.length > 0
                ? [
                    {
                      points: outsideTempSmoothed,
                      color: "#60a5fa",
                      strokeWidth: 1.4,
                      label: "Outside",
                    },
                  ]
                : []),
              ...(insideTempSmoothed.length > 0
                ? [
                    {
                      points: insideTempSmoothed,
                      color: "#f97316",
                      strokeWidth: 1.2,
                      label: "Cabin",
                    },
                  ]
                : []),
            ]}
            height={140}
            formatY={(v) => `${v.toFixed(0)} ${tempUnitSuffix}`}
            formatX={xTimeFmt}
            cursorX={cursorMs}
            onCursorChange={setCursorMs}
          />
          <div className="mt-2 flex items-center gap-3 text-[10px] text-neutral-500">
            {outsideTempSmoothed.length > 0 ? (
              <span className="flex items-center gap-1">
                <span className="inline-block w-2 h-2 rounded-sm bg-sky-400" />
                Outside
              </span>
            ) : null}
            {insideTempSmoothed.length > 0 ? (
              <span className="flex items-center gap-1">
                <span className="inline-block w-2 h-2 rounded-sm bg-orange-500" />
                Cabin
              </span>
            ) : null}
          </div>
        </Card>
      ) : null}

      <Card title="Route">
        {samples.isLoading ? (
          <Spinner />
        ) : mapPathSamples.length === 0 ? (
          <NoSamples />
        ) : (
          <DriveMap
            points={mapPoints}
            start={homeStart ?? { lat: drive.StartLat, lon: drive.StartLon }}
            end={homeEnd ?? { lat: drive.EndLat, lon: drive.EndLon }}
            height={360}
            cursorTime={cursorMs != null ? cursorMs / 1000 : null}
            onCursorChange={(t) =>
              setCursorMs(t != null ? Math.round(t * 1000) : null)
            }
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
