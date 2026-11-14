import { lazy, Suspense, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend, DETAIL_SAMPLE_LIMIT, type Sample } from "../lib/api";
import { forVehicle } from "../lib/vehicleSamples";

const LiveDriveMap = lazy(() => import("../map/LiveDriveMap").then((m) => ({ default: m.LiveDriveMap })));

// The drive in progress on a map (design_preview "live_v2"): the trail since the drive started, from the stored samples (no Rivian call), and
// the truck at its latest fix. Refreshed every 20 s while the tab is visible (React Query pauses an interval in a hidden tab).
export const LIVE_TRAIL_REFRESH_MS = 20_000;

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
      <Suspense fallback={<div className="h-[280px] animate-pulse rounded-lg border border-neutral-800 bg-neutral-900/50" />}>
        <LiveDriveMap points={points} />
      </Suspense>
    </div>
  );
}
