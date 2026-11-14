import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend, DETAIL_SAMPLE_LIMIT, type Charge, type Sample } from "../lib/api";
import { LineChart } from "./charts";
import { displayTimeZone, isActiveCharge } from "../lib/format";
import { formatTemperature, usePreferences } from "../lib/preferences";
import { smoothGaussianTime } from "../lib/smooth";
import { forVehicle } from "../lib/vehicleSamples";

// The open charge's power and battery over time, on the Live page (design_preview "live_v2"). The same curves as the charge page, read from
// the stored samples (no Rivian call), refreshed every 30 s while the tab is visible: React Query pauses an interval in a hidden tab and
// catches up when it is shown again. Only for a session that is genuinely open (isActiveCharge: an open state and a recent sample).
export const LIVE_GRAPH_REFRESH_MS = 30_000;

const xTimeFmt = (x: number) => new Date(x).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", timeZone: displayTimeZone() });

// A power axis that snaps to a common tier, as the charge page does.
function powerMax(peak: number): number {
  if (peak <= 0) return 12;
  for (const t of [12, 25, 60, 120, 200, 300, 400]) if (peak <= t * 0.92) return t;
  return Math.ceil((peak * 1.2) / 50) * 50;
}

export function LiveChargeGraph({ vehicleId }: { vehicleId: string }) {
  const { temperatureUnit } = usePreferences();
  const charges = useQuery<Charge[]>({
    queryKey: ["charges", "open", vehicleId],
    queryFn: () => backend.charges(5),
    refetchInterval: 60_000,
    staleTime: 30_000,
  });
  const open = charges.data?.find((c) => c.VehicleID === vehicleId && isActiveCharge(c));
  const startedMs = open ? new Date(open.StartedAt).getTime() : NaN;

  const samples = useQuery<Sample[]>({
    queryKey: ["samples", "live-charge", vehicleId, open?.ID],
    enabled: !!open && Number.isFinite(startedMs),
    queryFn: () => backend.samples(new Date(startedMs - 60_000), DETAIL_SAMPLE_LIMIT, new Date(Date.now() + 60_000), vehicleId).then((r) => forVehicle(r, vehicleId)),
    refetchInterval: LIVE_GRAPH_REFRESH_MS,
    refetchIntervalInBackground: false,
    staleTime: 10_000,
  });

  const data = useMemo(() => {
    const rows = (samples.data ?? []).filter((p) => new Date(p.At).getTime() >= startedMs - 60_000);
    const at = (p: Sample) => new Date(p.At).getTime();
    const soc = smoothGaussianTime(rows.filter((p) => p.BatteryLevelPct > 0).map((p) => ({ x: at(p), y: p.BatteryLevelPct })), 30_000);
    const power = smoothGaussianTime(rows.filter((p) => p.ChargerPowerKW > 0).map((p) => ({ x: at(p), y: p.ChargerPowerKW })), 45_000);
    // The pack's cell temperature: absent or 0 on a row recorded while the car slept (the charge page drops the same sentinel).
    const withPack = rows.filter((p) => Number.isFinite(p.pack_temp_avg_c) && (p.pack_temp_avg_c ?? 0) !== 0);
    const last = withPack[withPack.length - 1];
    return { soc, power, pack: last ? { avg: last.pack_temp_avg_c as number, min: last.pack_temp_min_c, max: last.pack_temp_max_c } : null };
  }, [samples.data, startedMs]);

  if (!open || data.soc.length < 2) return null;
  const peak = data.power.reduce((m, p) => Math.max(m, p.y), 0);
  const socLo = Math.min(...data.soc.map((p) => p.y));
  const socHi = Math.max(...data.soc.map((p) => p.y));
  const fmt = (c: number | undefined) => formatTemperature(c ?? null, temperatureUnit, 0);

  return (
    <div data-testid="live-charge-graph" className="mt-3 rounded-lg border border-neutral-800 bg-neutral-900/40 p-3">
      <div className="mb-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <div className="text-[11px] uppercase tracking-wide text-neutral-400">This charge so far</div>
        {data.pack ? (
          <div data-testid="live-pack-temp" className="text-xs text-neutral-300" title="Battery pack temperature: average, with the coolest and warmest cell group">
            <span className="text-neutral-500">Pack </span>
            <span className="tabular-nums font-medium" style={{ color: "#a78bfa" }}>{fmt(data.pack.avg)}</span>
            {data.pack.min !== undefined && data.pack.max !== undefined && data.pack.max !== 0 ? (
              <span className="tabular-nums text-neutral-500"> · {fmt(data.pack.min)} to {fmt(data.pack.max)}</span>
            ) : null}
          </div>
        ) : null}
      </div>
      <div className="mb-1 flex flex-wrap gap-3 text-[11px]">
        <span className="inline-flex items-center gap-1.5 text-neutral-400"><i className="inline-block h-0.5 w-3 rounded" style={{ background: "#10b981" }} />Battery</span>
        {data.power.length > 0 ? <span className="inline-flex items-center gap-1.5 text-neutral-400"><i className="inline-block h-0.5 w-3 rounded" style={{ background: "#f59e0b" }} />Power</span> : null}
      </div>
      <LineChart
        ariaLabel="Charging power and battery level over this charge"
        height={180}
        series={[
          { points: data.soc, color: "#10b981", strokeWidth: 1.4, area: true, label: "Battery" },
          ...(data.power.length > 0 ? [{ points: data.power, color: "#f59e0b", strokeWidth: 1.2, label: "Power", axis: "right" as const }] : []),
        ]}
        yDomain={[Math.max(0, Math.floor(socLo) - 3), Math.min(100, Math.ceil(socHi) + 3)]}
        y2Domain={[0, powerMax(peak)]}
        formatY={(v) => `${v.toFixed(0)}%`}
        formatY2={(v) => `${v.toFixed(0)} kW`}
        xTicks={3}
        formatX={xTimeFmt}
      />
    </div>
  );
}
