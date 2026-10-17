import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { Card, ErrorBox } from "./ui";
import { EfficiencyBars } from "./EfficiencyBars";
import { effUnit, effValue, hasValues } from "../lib/efficiency";
import { usePreferences } from "../lib/preferences";

// Activity → Efficiency (ui_theme_next): mi/kWh by outside temperature, speed and drive mode for
// the period, as three distribution charts under a headline average. "Not enough data" says so
// instead of drawing empty charts.
export function EfficiencyView({ vehicleID, since, until }: { vehicleID: string | undefined; since?: string; until?: string }) {
  const { distanceUnit } = usePreferences();
  const q = useQuery({
    queryKey: ["efficiency", vehicleID ?? "", since ?? "", until ?? ""],
    queryFn: () => backend.efficiency(vehicleID as string, since, until),
    enabled: !!vehicleID,
    staleTime: 60_000,
    retry: 1,
  });
  if (!vehicleID || q.isLoading) {
    return <div data-eff-state="loading" className="h-48 animate-pulse rounded-xl border border-neutral-800 bg-neutral-900/50" aria-label="Loading efficiency" />;
  }
  if (q.isError) return <ErrorBox title="Couldn't load efficiency" detail={String(q.error)} />;
  const d = q.data;
  if (d == null) {
    return (
      <Card>
        <p data-eff-state="unavailable" className="text-sm text-neutral-400">Efficiency breakdowns aren't available on this server yet.</p>
      </Card>
    );
  }
  const any = hasValues(d.by_temp) || hasValues(d.by_speed) || hasValues(d.by_mode);
  if (!d.enough_data || !any) {
    return (
      <Card>
        <div data-eff-state="not-enough" className="space-y-1">
          <p className="text-sm font-medium text-neutral-100">Not enough data yet</p>
          <p className="text-sm text-neutral-400">
            Efficiency needs a few drives with recorded energy in this period
            {d.min_drives ? ` (at least ${d.min_drives} drives in a bucket)` : ""}. Pick a longer period, or check back after more driving.
          </p>
          {d.overall?.drives ? (
            <p className="text-xs text-neutral-500">{d.overall.drives} {d.overall.drives === 1 ? "drive" : "drives"} so far in this period.</p>
          ) : null}
        </div>
      </Card>
    );
  }
  const avg = d.overall?.mi_per_kwh ?? null;
  return (
    <div className="space-y-4" data-eff-state="ready">
      <Card>
        <div className="flex flex-wrap items-end gap-x-6 gap-y-2">
          <div>
            <p className="ui-label">Average efficiency</p>
            <p data-eff-overall className="ui-bignum text-4xl font-semibold tabular-nums text-neutral-50">
              {avg != null ? effValue(avg, distanceUnit).toFixed(2) : "—"}
              <span className="ml-1.5 text-sm font-normal text-neutral-400">{effUnit(distanceUnit)}</span>
            </p>
          </div>
          {d.overall && (
            <p className="pb-1 text-xs tabular-nums text-neutral-500">
              {d.overall.drives} {d.overall.drives === 1 ? "drive" : "drives"} · {Math.round(distanceUnit === "km" ? d.overall.miles * 1.609344 : d.overall.miles)} {distanceUnit} · {d.overall.kwh.toFixed(0)} kWh
            </p>
          )}
        </div>
      </Card>
      <div className="grid gap-4 lg:grid-cols-2" data-eff-charts>
        {hasValues(d.by_temp) && (
          <Card title="By outside temperature">
            <EfficiencyBars kind="temp" buckets={d.by_temp!} overall={avg} minDrives={d.min_drives} />
          </Card>
        )}
        {hasValues(d.by_speed) && (
          <Card title="By speed">
            <EfficiencyBars kind="speed" buckets={d.by_speed!} overall={avg} minDrives={d.min_drives} />
          </Card>
        )}
        {hasValues(d.by_mode) && (
          <Card title="By drive mode">
            <EfficiencyBars kind="mode" buckets={d.by_mode!} overall={avg} minDrives={d.min_drives} />
          </Card>
        )}
      </div>
    </div>
  );
}
