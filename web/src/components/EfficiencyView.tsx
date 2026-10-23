import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { Card, ErrorBox } from "./ui";
import { EfficiencyBars } from "./EfficiencyBars";
import { hasFigure } from "../lib/efficiency";
import { usePreferences } from "../lib/preferences";

// Activity → Efficiency (ui_theme_next): efficiency by outside temperature, speed and drive mode for
// the period, as three distribution charts under a headline average. The server sends values and
// labels in the requested units. "Not enough data" says so instead of drawing empty charts.
export function EfficiencyView({ rivianVehicleID, since, until }: { rivianVehicleID: string | undefined; since?: string; until?: string }) {
  const { distanceUnit } = usePreferences();
  const units = distanceUnit === "km" ? "metric" : "imperial";
  const q = useQuery({
    queryKey: ["efficiency", rivianVehicleID ?? "", units, since ?? "", until ?? ""],
    queryFn: () => backend.efficiency(rivianVehicleID as string, units, since, until),
    enabled: !!rivianVehicleID,
    staleTime: 60_000,
    retry: 1,
  });
  if (!rivianVehicleID || q.isLoading) {
    return <div data-eff-state="loading" className="h-48 animate-pulse rounded-xl border border-neutral-800 bg-neutral-900/50" aria-label="Loading efficiency" />;
  }
  if (q.isError) return <ErrorBox title="Couldn't load efficiency" detail={String(q.error)} />;
  const d = q.data;
  if (d == null) {
    return (
      <Card>
        <p data-eff-state="unavailable" className="text-sm text-neutral-400">Efficiency insights aren't available on this server yet.</p>
      </Card>
    );
  }
  const groups = [
    { title: "By outside temperature", buckets: d.by_temperature },
    { title: "By speed", buckets: d.by_speed },
    { title: "By drive mode", buckets: d.by_drive_mode },
  ].filter((g) => g.buckets?.some(hasFigure));
  if (d.total.efficiency == null || d.total.not_enough_data || groups.length === 0) {
    return (
      <Card>
        <div data-eff-state="not-enough" className="space-y-1">
          <p className="text-sm font-medium text-neutral-100">Not enough data yet</p>
          <p className="text-sm text-neutral-400">
            Efficiency needs a few drives with recorded energy in this period (at least {Math.round(d.min_bucket_distance)} {d.distance_unit}). Pick a longer period, or check back after more driving.
          </p>
          {d.total.drives > 0 && (
            <p className="text-xs text-neutral-500">{d.total.drives} {d.total.drives === 1 ? "drive" : "drives"} · {Math.round(d.total.distance)} {d.distance_unit} so far in this period.</p>
          )}
        </div>
      </Card>
    );
  }
  const avg = d.total.efficiency;
  const ex = d.excluded;
  const excluded = ex ? (ex.no_energy ?? 0) + (ex.duplicates ?? 0) : 0;
  const trailer = ex?.trailer ?? 0;
  const wt = d.with_trailer && d.with_trailer.drives > 0 ? d.with_trailer : null;
  return (
    <div className="space-y-4" data-eff-state="ready">
      <Card>
        <div className="flex flex-wrap items-end gap-x-6 gap-y-2">
          <div>
            <p className="ui-label">Average efficiency</p>
            <p data-eff-overall className="ui-bignum text-4xl font-semibold tabular-nums text-neutral-50">
              {avg.toFixed(2)}
              <span className="ml-1.5 text-sm font-normal text-neutral-400">{d.efficiency_unit}</span>
            </p>
          </div>
          <p className="pb-1 text-xs tabular-nums text-neutral-500">
            {d.total.drives} {d.total.drives === 1 ? "drive" : "drives"} · {Math.round(d.total.distance).toLocaleString()} {d.distance_unit} · {d.total.energy_kwh.toFixed(0)} kWh
            {excluded > 0 && ` · ${excluded} left out (no energy recorded)`}
          </p>
        </div>
        {trailer > 0 && (
          <p data-eff-trailer className="mt-2 text-xs text-neutral-500">
            Excludes {trailer} {trailer === 1 ? "drive" : "drives"} with a trailer
            {wt && wt.efficiency != null ? `, which ran ${wt.efficiency.toFixed(2)} ${d.efficiency_unit} over ${Math.round(wt.distance).toLocaleString()} ${d.distance_unit}` : ""}.
            The truck reports a trailer connector, not what is on it, so a bike rack with lights can count; you can correct a drive on its page.
          </p>
        )}
      </Card>
      <div className="grid gap-4 lg:grid-cols-2" data-eff-charts>
        {groups.map((g) => (
          <Card key={g.title} title={g.title}>
            <EfficiencyBars buckets={g.buckets} overall={avg} unit={d.efficiency_unit} minDistance={d.min_bucket_distance} distanceUnit={d.distance_unit} />
          </Card>
        ))}
      </div>
    </div>
  );
}
