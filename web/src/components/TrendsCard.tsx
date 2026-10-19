import { useQuery } from "@tanstack/react-query";
import { backend, type TruckTrends } from "../lib/api";
import { usePreferences } from "../lib/preferences";
import { BarChart, LineChart } from "./charts";
import { specKwh } from "./PackHealthCard";
import { Card } from "./ui";

const monthFmt = new Intl.DateTimeFormat(undefined, { month: "short", year: "2-digit", timeZone: "UTC" });
const monthLabel = (ym: string): string => monthFmt.format(new Date(`${ym}-15T12:00:00Z`));
const dayFmt = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });

const note = "text-sm text-neutral-400";

// "Trends for your truck" (Truck tab): efficiency by month and the estimated usable capacity, for this truck only.
// A chart is drawn only when the server's own rule is met; otherwise the card says how far along it is, so there is
// never an empty chart or a trend from two data points.
export function TrendsCard({ vehicleID }: { vehicleID: string }) {
  const { distanceUnit } = usePreferences();
  const units = distanceUnit === "km" ? "metric" : "imperial";
  const q = useQuery({
    queryKey: ["trends", vehicleID, units],
    queryFn: () => backend.trends(vehicleID, units),
    enabled: !!vehicleID,
    staleTime: 5 * 60_000,
    retry: 1,
  });
  if (q.data === null) return null; // a server without the endpoint
  return (
    <Card title="Trends for your truck" span="full">
      <div data-trends className="space-y-6">
        {q.isPending && <div className="h-40 animate-pulse rounded-sm bg-neutral-900/50" />}
        {q.isError && (
          <p role="status" className={note}>
            Couldn&apos;t load trends.{" "}
            <button type="button" onClick={() => void q.refetch()} className="text-emerald-300 underline underline-offset-2">
              Try again
            </button>
          </p>
        )}
        {q.data && (
          <>
            <EfficiencyTrend t={q.data} />
            <CapacityTrend t={q.data} />
          </>
        )}
      </div>
    </Card>
  );
}

function EfficiencyTrend({ t }: { t: TruckTrends }) {
  const rows = t.efficiency;
  const enough = t.qualifying_months >= t.min_months;
  return (
    <section data-trend="efficiency" aria-labelledby="trend-eff">
      <h3 id="trend-eff" className="text-sm font-medium text-neutral-100">
        Efficiency by month
      </h3>
      {enough ? (
        <>
          <BarChart
            ariaLabel={`Efficiency by month, ${t.efficiency_unit}`}
            data={rows.map((r) => ({ label: monthLabel(r.month), value: r.distance >= t.min_month_distance && r.efficiency ? r.efficiency : 0 }))}
            height={140}
            formatY={(v) => v.toFixed(1)}
            formatValue={(v) => (v > 0 ? `${v.toFixed(2)} ${t.efficiency_unit}` : "not enough driving")}
          />
          <p className="mt-1 text-xs text-neutral-500">
            {t.efficiency_unit}, pack-side. Months under {t.min_month_distance} {t.distance_unit} of driving are left blank. It includes weather and driving style, so cold months usually run lower.
          </p>
        </>
      ) : (
        <p data-trend-state="sparse" className={`mt-1 ${note}`}>
          Not enough driving yet for a trend: {t.qualifying_months} of {t.min_months} months with at least {t.min_month_distance} {t.distance_unit}. This fills in as you drive.
        </p>
      )}
    </section>
  );
}

function CapacityTrend({ t }: { t: TruckTrends }) {
  const c = t.capacity;
  const head = (
    <h3 id="trend-cap" className="text-sm font-medium text-neutral-100">
      Estimated usable capacity (from your charges)
    </h3>
  );
  if (!c) {
    return (
      <section data-trend="capacity" data-trend-state="untracked" aria-labelledby="trend-cap">
        {head}
        <p className={`mt-1 ${note}`}>Battery capacity can&apos;t be tracked on this truck yet.</p>
      </section>
    );
  }
  const need = c.min_soc_delta_pct ?? 30;
  if (c.median_kwh == null || c.samples.length < 2) {
    return (
      <section data-trend="capacity" data-trend-state="sparse" aria-labelledby="trend-cap">
        {head}
        <p className={`mt-1 ${note}`}>
          Needs about {c.min_samples} charges that add at least {need}% of battery, across at least {c.min_span_days} days: {c.qualifying_count} so far.
        </p>
      </section>
    );
  }
  const pts = c.samples.map((s) => ({ x: new Date(s.at).getTime(), y: s.kwh_effective }));
  const x0 = Math.min(...pts.map((p) => p.x));
  const x1 = Math.max(...pts.map((p) => p.x));
  const spec = specKwh(c.spec_kwh ?? 0);
  const ys = [...pts.map((p) => p.y), c.median_kwh, ...(spec > 0 ? [spec] : [])];
  const lo = Math.floor(Math.min(...ys) - 2);
  const hi = Math.ceil(Math.max(...ys) + 2);
  return (
    <section data-trend="capacity" data-trend-state="ok" aria-labelledby="trend-cap">
      {head}
      <p className="mt-1 text-2xl font-semibold tabular-nums text-neutral-100">
        About {c.median_kwh.toFixed(0)} kWh <span className="text-sm font-normal text-neutral-400">usable (estimate)</span>
      </p>
      <LineChart
        ariaLabel="Estimated usable capacity over time, kWh"
        height={140}
        yDomain={[lo, hi]}
        xDomain={[x0, x1]}
        formatY={(v) => `${v.toFixed(0)}`}
        formatX={(x) => dayFmt.format(new Date(x))}
        series={[
          { points: pts, color: "#34d399", strokeWidth: 1.5, curve: "linear", label: "Estimate", formatCursor: (y) => `${y.toFixed(1)} kWh` },
          { points: [{ x: x0, y: c.median_kwh }, { x: x1, y: c.median_kwh }], color: "#a3a3a3", dash: "4 3", strokeWidth: 1, curve: "linear", label: "Median", formatCursor: (y) => `${y.toFixed(1)} kWh` },
          ...(spec > 0 ? [{ points: [{ x: x0, y: spec }, { x: x1, y: spec }], color: "#737373", dash: "1 3", strokeWidth: 1, curve: "linear" as const, label: `Rivian spec: ${spec.toFixed(0)} kWh`, formatCursor: (y: number) => `${y.toFixed(0)} kWh` }] : []),
        ]}
      />
      <p className="mt-1 text-xs text-neutral-500">
        Estimated from charges that add at least {need}% of battery. It moves a little with temperature and is not a Rivian figure. Charging losses and how the truck reports charge level can shift it by a few percent.
        {spec > 0 && ` Rivian's spec for this pack: ${spec.toFixed(0)} kWh.`}
      </p>
    </section>
  );
}
