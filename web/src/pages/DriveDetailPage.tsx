import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { backend, ApiError, type DriveRecap, type DriveWeather, type Sample } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { LineChart } from "../components/charts";
import { DriveMap } from "../components/DriveMap";
import {
  durationSeconds,
  formatDateTime,
  formatDuration,
  maxFixAgeSeconds,
  num,
  pct,
} from "../lib/format";
import { smoothGaussianTime } from "../lib/smooth";
import { collapseRoundTrips } from "../lib/drives";
import { usePreferences, formatTemperature } from "../lib/preferences";
import { useAIEnabled } from "../lib/config";

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
        // Speed in mph for the speed-colored polyline. DriveMap
        // segments the line by this value; absence falls back to
        // a uniform emerald stroke.
        s: p.SpeedMph,
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
  // Speed chart anchor: a drive's last in-motion sample is by
  // definition non-zero (the trip ends because the car shifts out
  // of D, not because it crawls to 0 mph). The 60 s post-end pad in
  // driveSamples gives the line space to drop, but only if a parked
  // sample lands inside that window — and Rivian's telemetry often
  // doesn't write one for several minutes after parking. So when
  // the visible tail is still moving, append a synthetic 0-mph
  // sample 1 s after the last in-motion point. Speed chart only —
  // we don't fabricate SoC.
  const speedPts = (() => {
    if (speedPtsRaw.length === 0) return speedPtsRaw;
    const last = speedPtsRaw[speedPtsRaw.length - 1];
    if ((last.y ?? 0) <= 0.5) return speedPtsRaw;
    return [...speedPtsRaw, { x: last.x + 1000, y: 0 }];
  })();
  const socPts = smoothGaussianTime(socPtsRaw, 30_000);

  // Elevation series. Sourced from the Mapzen Terrarium DEM lookup
  // the recorder runs at sample-write time (samples.altitude_m). The
  // rest of this page is imperial (mph, mi, °F-or-°C) so render the
  // y axis in feet for consistency with speed/range. Light smoothing
  // irons out the ~1 m quantisation noise from the DEM's int16
  // encoding without flattening real grade. When every sample in the
  // window lacks altitude (legacy drives, ElectraFi imports, recorder
  // offline, cold-cache misses) the panel hides itself.
  const elevPtsRaw = driveSamples
    .filter((p) => typeof p.altitude_m === "number" && Number.isFinite(p.altitude_m))
    .map((p) => ({
      x: new Date(p.At).getTime(),
      y: (p.altitude_m as number) * 3.28084,
    }));
  const elevPts = smoothGaussianTime(elevPtsRaw, 15_000);
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
  // The combined chart can only fit one extra dotted line, so pick
  // whichever temperature signal we actually have. Rivian's live WS
  // feed only exposes cabin temp (outside is hardcoded to 0 in
  // internal/rivian/live.go); ElectraFi historical imports carry
  // outside but not cabin. Prefer outside when available — it's the
  // ambient driver of range — and fall back to cabin so a live
  // session still shows something rather than nothing.
  const ambientTempSeries =
    outsideTempSmoothed.length > 1
      ? { points: outsideTempSmoothed, label: "Outside temp" }
      : insideTempSmoothed.length > 1
        ? { points: insideTempSmoothed, label: "Cabin temp" }
        : null;

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

      <Card title="Telemetry">
        {samples.isLoading ? (
          <Spinner />
        ) : speedPts.length === 0 && socPts.length === 0 ? (
          <NoSamples />
        ) : (
          (() => {
            // Shared x-domain so all stacked panels align tick-for-tick
            // and the synced cursor lands on the same moment in each
            // chart. Pulled from the widest series we render (speed
            // includes the synthetic 0-mph anchor at the tail).
            const allX = [
              ...speedPts.map((p) => p.x),
              ...socPts.map((p) => p.x),
              ...(ambientTempSeries?.points.map((p) => p.x) ?? []),
              ...elevPts.map((p) => p.x),
            ];
            const xDomain: [number, number] | undefined =
              allX.length > 0
                ? [Math.min(...allX), Math.max(...allX)]
                : undefined;
            // Bottom panel of the stack draws x-axis ticks; everything
            // above suppresses them. Order is speed → battery → temp
            // → elevation, so each panel only shows ticks when no
            // panel below it is rendered for this drive.
            const hasTemp = !!ambientTempSeries;
            const hasElev = elevPts.length > 1;
            return (
              <div className="space-y-3">
                {speedPts.length > 0 ? (
                  <ChartPanel label="Speed" colorClass="bg-sky-400">
                    <LineChart
                      series={[
                        {
                          points: speedPts,
                          color: "#38bdf8",
                          strokeWidth: 1.4,
                          area: true,
                          curve: "monotone",
                          label: "Speed",
                        },
                      ]}
                      height={100}
                      xDomain={xDomain}
                      yDomain={[0, Math.max(50, drive.MaxSpeedMph + 5)]}
                      formatY={(v) => `${v.toFixed(0)} mph`}
                      formatX={xTimeFmt}
                      xTicks={0}
                      cursorX={cursorMs}
                      onCursorChange={setCursorMs}
                    />
                  </ChartPanel>
                ) : null}
                {socPts.length > 0 ? (
                  <ChartPanel label="Battery" colorClass="bg-emerald-500">
                    <LineChart
                      series={[
                        {
                          points: socPts,
                          color: "#10b981",
                          strokeWidth: 1.4,
                          label: "Battery",
                        },
                      ]}
                      height={90}
                      xDomain={xDomain}
                      yDomain={[
                        Math.max(0, drive.EndSoCPct - 5),
                        Math.min(100, drive.StartSoCPct + 5),
                      ]}
                      formatY={(v) => `${v.toFixed(0)}%`}
                      formatX={xTimeFmt}
                      xTicks={hasTemp || hasElev ? 0 : 4}
                      cursorX={cursorMs}
                      onCursorChange={setCursorMs}
                    />
                  </ChartPanel>
                ) : null}
                {hasTemp ? (
                  <ChartPanel
                    label={ambientTempSeries.label}
                    colorClass="bg-orange-400"
                  >
                    <LineChart
                      series={[
                        {
                          points: ambientTempSeries.points,
                          color: "#fb923c",
                          strokeWidth: 1.2,
                          curve: "monotone",
                          label: ambientTempSeries.label,
                          formatCursor: (v: number) =>
                            `${v.toFixed(0)}${tempUnitSuffix}`,
                        },
                      ]}
                      height={70}
                      xDomain={xDomain}
                      formatY={(v) => `${v.toFixed(0)}${tempUnitSuffix}`}
                      formatX={xTimeFmt}
                      yTicks={2}
                      xTicks={hasElev ? 0 : 4}
                      cursorX={cursorMs}
                      onCursorChange={setCursorMs}
                    />
                  </ChartPanel>
                ) : null}
                {hasElev ? (
                  <ChartPanel label="Elevation" colorClass="bg-violet-400">
                    <LineChart
                      series={[
                        {
                          points: elevPts,
                          color: "#a78bfa",
                          strokeWidth: 1.3,
                          area: true,
                          curve: "monotone",
                          label: "Elevation",
                          formatCursor: (v: number) =>
                            `${v.toFixed(0)} ft`,
                        },
                      ]}
                      height={80}
                      xDomain={xDomain}
                      formatY={(v) => `${v.toFixed(0)} ft`}
                      formatX={xTimeFmt}
                      yTicks={2}
                      xTicks={4}
                      cursorX={cursorMs}
                      onCursorChange={setCursorMs}
                    />
                  </ChartPanel>
                ) : null}
              </div>
            );
          })()
        )}
      </Card>

      <Card
        title="Route"
        actions={
          hasEndpointPair(drive) ? (
            <a
              href={googleRouteURL(drive)}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 rounded-md border border-emerald-700/60 bg-emerald-900/30 px-2.5 py-1 text-xs font-medium text-emerald-300 hover:bg-emerald-900/50 hover:text-emerald-200"
              title="Open route in Google Maps"
            >
              Google Maps
              <span aria-hidden>↗</span>
            </a>
          ) : null
        }
      >
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
            fixAgeSeconds={maxFixAgeSeconds(mapPathSamples)}
          />
        )}
      </Card>

      {samples.isError ? (
        <ErrorBox
          title="Sample data unavailable"
          detail={String(samples.error)}
        />
      ) : null}

      {drive ? <TripRecapCard driveId={drive.ID} /> : null}
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

function NoSamples() {
  return (
    <p className="text-sm text-neutral-500">
      No raw samples stored for this time window. Live ingestion isn't
      wired yet; ElectraFi samples only land in the <code>samples</code>{" "}
      table for runs after the importer was added.
    </p>
  );
}

// One row of the synced telemetry stack: a small label header above
// a fixed-height LineChart. Kept here (not in components/charts.tsx)
// because so far DriveDetailPage is the only caller; promote when a
// second page wants the same treatment.
function ChartPanel({
  label,
  colorClass,
  children,
}: {
  label: string;
  colorClass: string;
  children: import("react").ReactNode;
}) {
  return (
    <div>
      <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wide text-neutral-400">
        <span className={`inline-block w-2 h-2 rounded-sm ${colorClass}`} />
        {label}
      </div>
      {children}
    </div>
  );
}

// TripRecapCard renders the AI-generated 2-3 sentence narration of
// a drive. The card hides itself entirely when no AI provider is
// configured (operator hasn't added a key in Settings → AI), so a
// fresh self-hosted install with no AI key sees no dead button.
//
// Generation is on-demand and cached per (user, drive) on the
// server; finished drives are immutable, so a recap stays valid
// until the user explicitly regenerates. The Regenerate path POSTs
// with force=1 and re-bills the operator's LLM key.
//
// We keep generation state in a local useState rather than
// react-query's mutation API because nothing else in this codebase
// uses mutations and pulling the dep in for one place would be
// inconsistent with the rest of the data layer.
function TripRecapCard({ driveId }: { driveId: string }) {
  const enabled = useAIEnabled();
  const cached = useQuery({
    queryKey: ["drive-recap", driveId],
    enabled,
    retry: false,
    queryFn: async (): Promise<DriveRecap | null> => {
      try {
        return await backend.driveRecapGet(driveId);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
  });
  const [busy, setBusy] = useState(false);
  const [genErr, setGenErr] = useState<string | null>(null);

  if (!enabled) return null;

  async function generate(force: boolean) {
    setBusy(true);
    setGenErr(null);
    try {
      const fresh = await backend.driveRecapGenerate(driveId, force);
      cached.refetch();
      // refetch is fire-and-forget; we also seed the local query
      // cache result by writing to the in-flight data via refetch's
      // promise so the UI flips even before the GET round-trip.
      void fresh;
    } catch (e) {
      setGenErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const data = cached.data;
  const showSpinner = cached.isLoading || busy;

  return (
    <Card
      title="Trip recap"
      actions={
        data && !busy ? (
          <button
            type="button"
            onClick={() => generate(true)}
            className="text-xs text-neutral-400 hover:text-neutral-200 underline-offset-2 hover:underline"
            title="Regenerate the recap (re-bills your AI provider)"
          >
            Regenerate
          </button>
        ) : null
      }
    >
      {showSpinner ? (
        <div className="flex items-center gap-2 text-sm text-neutral-400">
          <Spinner />
          {busy ? "Writing recap…" : null}
        </div>
      ) : genErr ? (
        <ErrorBox title="Recap generation failed" detail={genErr} />
      ) : !data ? (
        <div className="space-y-3">
          <p className="text-sm text-neutral-400">
            Generate a 2–3 sentence summary of this drive using your
            configured AI provider. The drive's summary stats and
            elevation profile are sent — no GPS coordinates or
            per-second telemetry leave the box.
          </p>
          <button
            type="button"
            onClick={() => generate(false)}
            className="rounded-md border border-emerald-700/60 bg-emerald-900/30 px-3 py-1.5 text-sm font-medium text-emerald-300 hover:bg-emerald-900/50 hover:text-emerald-200"
          >
            Generate trip recap
          </button>
        </div>
      ) : (
        <RecapBody data={data} />
      )}
    </Card>
  );
}

// RecapBody renders the structured shape (headline + body + highlight
// chips + mood chip) when the backend returned it, and falls back to
// the legacy plain-prose layout when only `recap` is present (older
// cached rows pre-dating the JSON migration). Keeping both paths in
// one component means the card never flickers between layouts when a
// user re-generates a stale row.
function RecapBody({ data }: { data: DriveRecap }) {
  const hasStructured =
    !!data.headline ||
    !!data.body ||
    (data.highlights && data.highlights.length > 0);
  return (
    <div className="space-y-3">
      {data.weather ? <WeatherStrip weather={data.weather} /> : null}
      {hasStructured ? (
        <>
          {data.headline ? (
            <p className="text-base font-semibold leading-snug text-neutral-100">
              {data.headline}
            </p>
          ) : null}
          {data.body ? (
            <p className="text-sm leading-relaxed text-neutral-300">
              {data.body}
            </p>
          ) : null}
          {data.highlights && data.highlights.length > 0 ? (
            <div className="flex flex-wrap gap-2">
              {data.highlights.map((h, i) => (
                <div
                  key={`${h.label}-${i}`}
                  className="rounded-md border border-neutral-800 bg-neutral-900/60 px-2.5 py-1 text-[11px]"
                >
                  <span className="text-neutral-500">{h.label}</span>
                  <span className="ml-1.5 font-medium tabular-nums text-neutral-200">
                    {h.value}
                  </span>
                </div>
              ))}
            </div>
          ) : null}
        </>
      ) : (
        <p className="text-sm leading-relaxed text-neutral-200">
          {data.recap}
        </p>
      )}
      <div className="text-[11px] text-neutral-500 flex flex-wrap items-center gap-x-2">
        {data.mood ? (
          <span className="rounded-full border border-emerald-800/60 bg-emerald-900/20 px-2 py-0.5 text-emerald-300/90">
            {data.mood}
          </span>
        ) : null}
        <span>
          {data.model} · {formatDateTime(data.generated_at)}
          {data.cached ? " · cached" : ""}
        </span>
      </div>
    </div>
  );
}

// WeatherStrip renders a one-line summary of the captured weather at
// the trip's start above the recap text. Kept compact — temp,
// conditions, and wind (with headwind/tailwind annotation) — because
// the structured recap chips already break out anything the model
// thought was notable. Hidden when the snapshot has no usable
// fields, so a half-empty upstream response degrades cleanly.
function WeatherStrip({ weather }: { weather: DriveWeather }) {
  const parts: string[] = [];
  if (weather.temp_f != null) {
    if (weather.feels_like_f != null && Math.abs(weather.feels_like_f - weather.temp_f) >= 3) {
      parts.push(`${Math.round(weather.temp_f)}°F (feels ${Math.round(weather.feels_like_f)}°F)`);
    } else {
      parts.push(`${Math.round(weather.temp_f)}°F`);
    }
  }
  if (weather.conditions) parts.push(weather.conditions);
  if (weather.wind_mph != null && weather.wind_mph >= 1) {
    const dir =
      weather.wind_from_deg != null
        ? compass(weather.wind_from_deg)
        : "";
    let s = `${Math.round(weather.wind_mph)} mph wind${dir ? ` ${dir}` : ""}`;
    if (weather.headwind_mph != null && Math.abs(weather.headwind_mph) >= 3) {
      s += weather.headwind_mph > 0
        ? ` (${Math.round(weather.headwind_mph)} mph head)`
        : ` (${Math.round(-weather.headwind_mph)} mph tail)`;
    }
    parts.push(s);
  }
  if (weather.precip_in != null && weather.precip_in >= 0.01) {
    parts.push(`${weather.precip_in.toFixed(2)}" precip`);
  }
  if (parts.length === 0) return null;
  return (
    <div className="text-[11px] uppercase tracking-wide text-neutral-500">
      Weather at start
      <span className="ml-2 normal-case tracking-normal text-neutral-300">
        {parts.join(" · ")}
      </span>
    </div>
  );
}

// compass converts a degree heading (where the wind is coming FROM)
// to a 16-point compass label so users don't have to mentally map
// "270" to "W". Matches Open-Meteo's convention.
function compass(deg: number): string {
  const dirs = [
    "N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE",
    "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW",
  ];
  const i = Math.round(((deg % 360) / 22.5)) % 16;
  return dirs[i];
}
