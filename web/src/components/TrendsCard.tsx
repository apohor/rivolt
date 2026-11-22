import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ProLimitNotice } from "./ProLimitNotice";
import { proLimitOf, retryUnlessLimit } from "../lib/proLimit";
import { backend, type TruckTrends } from "../lib/api";
import { usePreferences } from "../lib/preferences";
import { BarChart } from "./charts";
import { Card } from "./ui";

const monthFmt = new Intl.DateTimeFormat(undefined, { month: "short", year: "2-digit", timeZone: "UTC" });
const monthLabel = (ym: string): string => monthFmt.format(new Date(`${ym}-15T12:00:00Z`));

// The Pro range: 12 or 24 months, or everything the server keeps (it returns only months with driving, so a long range is cheap).
const RANGES = [
  { months: 12, label: "12 months" },
  { months: 24, label: "24 months" },
  { months: 120, label: "All" },
] as const;

const note = "text-sm text-neutral-400";

// "Trends for your truck" (Truck tab): efficiency by month, for this truck only. (The capacity estimate's history
// is in the Battery capacity card.)
// A chart is drawn only when the server's own rule is met; otherwise the card says how far along it is, so there is
// never an empty chart or a trend from two data points.
export function TrendsCard({ vehicleID }: { vehicleID: string }) {
  const { distanceUnit } = usePreferences();
  const units = distanceUnit === "km" ? "metric" : "imperial";
  const [months, setMonths] = useState<number>(RANGES[0].months);
  const q = useQuery({
    queryKey: ["trends", vehicleID, units, months],
    queryFn: () => backend.trends(vehicleID, units, months),
    // Keep the chart on screen while another range of the same truck and units loads; never another truck's.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[1] === vehicleID && prevQuery.queryKey[2] === units ? prev : undefined),
    enabled: !!vehicleID,
    staleTime: 5 * 60_000,
    // A free account's 402 is an answer, not a failure: no retry.
    retry: retryUnlessLimit(1),
  });
  if (q.data === null) return null; // a server without the endpoint
  const limit = proLimitOf(q.error);
  if (limit) {
    return (
      <Card title="Trends for your truck" span="full">
        <ProLimitNotice limit={limit} />
      </Card>
    );
  }
  // Only a Pro account gets here with data (the route is Pro-only: a free account's 402 is the notice above), so the control is shown
  // once the first answer is in, and no range is asked for before the account has proved it can read trends.
  const ranges = q.data ? (
    <div className="inline-flex rounded-md border border-neutral-800 p-0.5 text-xs" role="group" aria-label="Range" data-trends-range>
      {RANGES.map((r) => (
        <button
          key={r.months}
          type="button"
          aria-pressed={months === r.months}
          onClick={() => setMonths(r.months)}
          className={`rounded px-2 py-0.5 ${months === r.months ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
        >
          {r.label}
        </button>
      ))}
    </div>
  ) : undefined;
  return (
    <Card title="Trends for your truck" span="full" actions={ranges}>
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
          </>
        )}
      </div>
    </Card>
  );
}

// The drives with a trailer over the window as one figure: ", which ran 1.40 mi/kWh over 120 mi", or nothing when it is thin.
function trailerSummary(t: TruckTrends): string {
  let miles = 0;
  let kwh = 0;
  for (const r of t.efficiency) {
    if (r.trailer) {
      miles += r.trailer.distance;
      kwh += r.trailer.energy_kwh;
    }
  }
  return kwh > 0 ? `, which ran ${(miles / kwh).toFixed(2)} ${t.efficiency_unit} over ${Math.round(miles).toLocaleString()} ${t.distance_unit}` : "";
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
          {(t.excluded_trailer_drives ?? 0) > 0 && (
            <p data-trend-trailer className="mt-1 text-xs text-neutral-500">
              Excludes {t.excluded_trailer_drives} {t.excluded_trailer_drives === 1 ? "drive" : "drives"} with a trailer
              {trailerSummary(t)}.
            </p>
          )}
        </>
      ) : (
        <p data-trend-state="sparse" className={`mt-1 ${note}`}>
          Not enough driving yet for a trend: {t.qualifying_months} of {t.min_months} months with at least {t.min_month_distance} {t.distance_unit}. This fills in as you drive.
        </p>
      )}
    </section>
  );
}
