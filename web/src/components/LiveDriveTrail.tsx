import { lazy, Suspense, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend, DETAIL_SAMPLE_LIMIT, type Sample } from "../lib/api";
import { forVehicle } from "../lib/vehicleSamples";
import { useDesignPreview } from "../lib/config";
import { usePreferences } from "../lib/preferences";
import { destinationLine, destinationRoute, type LiveDestination } from "../lib/liveDestination";

const LiveDriveMap = lazy(() => import("../map/LiveDriveMap").then((m) => ({ default: m.LiveDriveMap })));

// The drive in progress on a map (design_preview "live_v2"): the trail since the drive started, from the stored samples (no Rivian call), and
// the truck at its latest fix. Refreshed every 20 s while the tab is visible (React Query pauses an interval in a hidden tab).
export const LIVE_TRAIL_REFRESH_MS = 20_000;
// The destination (design_preview "live_destination") is asked for on the live cadence, only while this drive is on screen and the tab is
// visible (React Query pauses an interval in a hidden tab); the server allows 30 a minute.
export const LIVE_DESTINATION_REFRESH_MS = 5_000;

export function LiveDriveTrail({ vehicleId, startedAt, lat, lon }: { vehicleId: string; startedAt: string; lat?: number; lon?: number }) {
  const startedMs = new Date(startedAt).getTime();
  const samples = useQuery<Sample[]>({
    queryKey: ["samples", "live-drive", vehicleId, startedAt],
    enabled: Number.isFinite(startedMs),
    queryFn: () => backend.samples(new Date(startedMs - 60_000), DETAIL_SAMPLE_LIMIT, new Date(Date.now() + 60_000), vehicleId).then((r) => forVehicle(r, vehicleId)),
    refetchInterval: LIVE_TRAIL_REFRESH_MS,
    refetchIntervalInBackground: false,
    staleTime: 10_000,
  });
  const destOn = useDesignPreview("live_destination");
  const { distanceUnit } = usePreferences();
  // 204 (no trip) clears it; the first 404 means the feature is off for this account, and the asking stops. Nothing is kept once it is
  // off screen (gcTime 0) and the answer is never logged.
  const destQuery = useQuery<LiveDestination | null | "off">({
    queryKey: ["live-destination", vehicleId],
    enabled: destOn,
    queryFn: () => backend.liveDestination(vehicleId),
    refetchInterval: (q) => (q.state.data === "off" ? false : LIVE_DESTINATION_REFRESH_MS),
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    retry: false,
    gcTime: 0,
  });
  const dest = destQuery.data && destQuery.data !== "off" ? destQuery.data : null;
  const mapDest = useMemo(() => (dest ? { lat: dest.lat, lon: dest.lon, name: dest.name, route: destinationRoute(dest) } : null), [dest]);
  const points = useMemo(() => {
    const rows = (samples.data ?? []).filter((p) => new Date(p.At).getTime() >= startedMs - 60_000 && Number.isFinite(p.Lat) && Number.isFinite(p.Lon) && (p.Lat !== 0 || p.Lon !== 0));
    const trail = rows.map((p) => ({ lat: p.Lat, lon: p.Lon }));
    // The state's own fix is newer than the last stored sample: the truck's dot sits there.
    if (Number.isFinite(lat) && Number.isFinite(lon) && (lat !== 0 || lon !== 0)) trail.push({ lat: lat as number, lon: lon as number });
    return trail;
  }, [samples.data, startedMs, lat, lon]);

  if (points.length === 0) return null;
  return (
    <div className="mt-3">
      <div className="mb-2 text-[11px] uppercase tracking-wide text-neutral-400">This drive so far</div>
      {dest ? (
        <div className="mb-2 text-sm text-neutral-200" data-testid="live-destination">
          {destinationLine(dest, distanceUnit)}
        </div>
      ) : null}
      <Suspense fallback={<div className="h-[280px] animate-pulse rounded-lg border border-neutral-800 bg-neutral-900/50" />}>
        <LiveDriveMap points={points} destination={mapDest} />
      </Suspense>
    </div>
  );
}
