import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { backend, ApiError, type DriveWeatherSeries, type Sample } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { DriveMap } from "../components/DriveMap";
import { DriveTimeline } from "../components/DriveTimeline";
import { EfficiencyExplainerCard } from "../components/EfficiencyExplainerCard";
import {
  durationSeconds,
  formatDateTime,
  formatDuration,
  num,
  pct,
} from "../lib/format";
import { collapseRoundTrips } from "../lib/drives";
import { analyzeDrivePower } from "../lib/power";
import { useGPSThresholds } from "../lib/config";
import { usePreferences, formatTemperature } from "../lib/preferences";

export default function DriveDetailPage() {
  const { id } = useParams<{ id: string }>();
  const prefs = usePreferences();
  // Shared cursor for the Speed chart, Battery chart and route map.
  // Stored in milliseconds so it can be passed straight through as
  // the chart x-axis cursor; converted to unix seconds for the map.
  const [cursorMs, setCursorMs] = useState<number | null>(null);
  // Lifted zoom window: when set, the timeline shows only this slice
  // of the drive AND the route map crops to the same window. Cleared
  // by the timeline's reset button or a double-click on the chart;
  // also cleared whenever the URL/drive changes so a stale window
  // from drive A doesn't get applied to drive B.
  const [viewWindow, setViewWindow] = useState<[number, number] | null>(
    null,
  );
  useEffect(() => {
    setViewWindow(null);
    setCursorMs(null);
  }, [id]);
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

  // Time series for the weather panel (temperature line +
  // precipitation overlay). Returns an empty `points` array when the
  // drive has never been backfilled, so we don't have to special-case
  // the 404 path.
  const driveWeatherSeries = useQuery({
    queryKey: ["drive-weather-series", drive?.ID],
    enabled: !!drive,
    queryFn: async (): Promise<DriveWeatherSeries> => {
      try {
        return await backend.driveWeatherSeries(drive!.ID);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) {
          return { points: [] };
        }
        throw e;
      }
    },
    staleTime: 60_000,
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
    // Pre-filter by EndedAt + parked-frame trim (existing behavior),
    // then narrow to the chart's zoom window if one is set so the
    // route map crops to the same slice the user is inspecting.
    // fitBounds inside DriveMap auto-reframes to the new polyline.
    const inWindow = driveSamples.filter((p) => {
      const t = new Date(p.At).getTime();
      if (t > endMs) return false;
      if (viewWindow && (t < viewWindow[0] || t > viewWindow[1])) return false;
      return true;
    });
    let head = 0;
    while (head < inWindow.length && inWindow[head].ShiftState === "P") head++;
    let tail = inWindow.length;
    while (tail > head && inWindow[tail - 1].ShiftState === "P") tail--;
    return inWindow.slice(head, tail);
  }, [drive, driveSamples, viewWindow]);

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

  // Physics-based power totals — share the same model the timeline
  // ribbon uses so the "Regen recovered" stat tile and the chart's
  // green/red pixels are computed from one source of truth. Returns
  // null until the drive resolves so this hook can sit above the
  // early-return paths below — Rules of Hooks: every hook must run
  // in the same order on every render, and we used to break that by
  // declaring this useMemo *after* the `if (!drive) return …` block.
  // On a cold drive query (hard refresh), render 1 short-circuited
  // before the memo and render 2 added it back, which unmounts the
  // whole tree with "Rendered more hooks than during the previous
  // render" → blank page.
  const powerAnalysis = useMemo(() => {
    if (!drive) return null;
    return analyzeDrivePower(driveSamples, drive);
  }, [driveSamples, drive]);

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

  const duration = durationSeconds(drive.StartedAt, drive.EndedAt);
  const tempUnit = prefs.temperatureUnit;
  const weatherPts = driveWeatherSeries.data?.points ?? [];

  // Estimate GPS accuracy during the drive by checking fix freshness
  // and sample continuity. Returns true if accuracy is likely low.
  // Thresholds come from /api/config (admin-tunable).
  const gpsThresholds = useGPSThresholds();
  const gpsAccuracyLow = (() => {
    if (driveSamples.length === 0) return false;

    // Check 1: Percentage of samples with missing fix timestamps.
    const samplesWithoutFix = driveSamples.filter(
      (s) => !s.LocationFixAt
    ).length;
    const missingFixRatio = samplesWithoutFix / driveSamples.length;
    if (missingFixRatio > gpsThresholds.missingPct) return true;

    // Check 2: Max fix age during the drive (when a sample's
    // LocationFixAt is much older than its wall-clock At).
    let maxFixAgeS = 0;
    for (const s of driveSamples) {
      if (s.LocationFixAt) {
        const fixMs = new Date(s.LocationFixAt).getTime();
        const nowMs = new Date(s.At).getTime();
        const ageS = (nowMs - fixMs) / 1000;
        if (ageS > maxFixAgeS) maxFixAgeS = ageS;
      }
    }
    if (maxFixAgeS > gpsThresholds.staleSec) return true;

    // Check 3: Spatial jumps suggesting dropouts. Count consecutive
    // pairs that imply > 150 mph over > 0.5 mi; require at least
    // gpsThresholds.jumpCount to flag so a single GPS glitch doesn't
    // mark the whole drive as low-accuracy.
    const JUMP_THRESHOLD_MI = 0.5;
    let jumps = 0;
    for (let i = 1; i < driveSamples.length; i++) {
      const prev = driveSamples[i - 1];
      const curr = driveSamples[i];
      if (
        (prev.Lat !== 0 || prev.Lon !== 0) &&
        (curr.Lat !== 0 || curr.Lon !== 0)
      ) {
        const dy = (curr.Lat - prev.Lat) * 69; // miles per degree latitude
        const dx =
          (curr.Lon - prev.Lon) *
          69 *
          Math.cos(((prev.Lat + curr.Lat) / 2) * (Math.PI / 180));
        const distMi = Math.hypot(dx, dy);
        const timeMs =
          new Date(curr.At).getTime() - new Date(prev.At).getTime();
        const speedMph = (distMi / (timeMs / 3600000)) || 0;
        if (distMi > JUMP_THRESHOLD_MI && speedMph > 150) {
          jumps++;
          if (jumps >= gpsThresholds.jumpCount) return true;
        }
      }
    }
    return false;
  })();

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
          label="Regen recovered"
          value={
            powerAnalysis && powerAnalysis.regenKwh >= 0.05
              ? num(powerAnalysis.regenKwh, 2, "kWh")
              : "—"
          }
          hint={
            powerAnalysis &&
            powerAnalysis.regenKwh >= 0.05 &&
            powerAnalysis.regenPct > 0
              ? `${powerAnalysis.regenPct.toFixed(0)}% of consumption`
              : undefined
          }
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

      <Card title="Drive timeline">
        {samples.isLoading ? (
          <Spinner />
        ) : driveSamples.length === 0 ? (
          <NoSamples />
        ) : (
          <DriveTimeline
            drive={drive}
            samples={driveSamples}
            weatherPts={weatherPts}
            cursorMs={cursorMs}
            onCursorChange={setCursorMs}
            viewWindow={viewWindow}
            onViewWindowChange={setViewWindow}
            tempUnit={tempUnit}
          />
        )}
      </Card>

      <Card
        title="Route"
        actions={
          <div className="flex items-center gap-2">
            {gpsAccuracyLow ? (
              <span
                className="inline-flex items-center gap-1 rounded-md border border-amber-700/50 bg-amber-900/20 px-2 py-0.5 text-[11px] font-medium text-amber-300"
                title="Low GPS accuracy: dropouts, stale fixes, or signal loss detected during this drive. The plotted route may not reflect the actual path."
              >
                <span aria-hidden>⚠</span>
                Low GPS accuracy
              </span>
            ) : null}
            {hasEndpointPair(drive) ? (
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
            ) : null}
          </div>
        }
      >
        {samples.isLoading ? (
          <Spinner />
        ) : mapPathSamples.length === 0 ? (
          <NoSamples />
        ) : (
          <DriveMap
            points={mapPoints}
            // When the timeline is zoomed, suppress the home start/end
            // pins. Otherwise DriveMap extends the polyline from the
            // drive's true endpoints through the cropped window and
            // fitBounds includes those far-away anchors — which drags
            // the visible map back out to the whole drive instead of
            // framing just the cropped slice.
            start={
              viewWindow
                ? undefined
                : (homeStart ?? { lat: drive.StartLat, lon: drive.StartLon })
            }
            end={
              viewWindow
                ? undefined
                : (homeEnd ?? { lat: drive.EndLat, lon: drive.EndLon })
            }
            height={360}
            cursorTime={cursorMs != null ? cursorMs / 1000 : null}
            onCursorChange={(t) =>
              setCursorMs(t != null ? Math.round(t * 1000) : null)
            }
            fixAgeSeconds={null}
          />
        )}
      </Card>

      {samples.isError ? (
        <ErrorBox
          title="Sample data unavailable"
          detail={String(samples.error)}
        />
      ) : null}

      {drive ? <EfficiencyExplainerCard driveId={drive.ID} /> : null}
    </div>
  );
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
      No telemetry was recorded for this drive.
    </p>
  );
}
