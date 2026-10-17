import type { EfficiencyBucket } from "../lib/api";
import { bucketLabel, effUnit, effValue, scaleMax } from "../lib/efficiency";
import { usePreferences } from "../lib/preferences";

// One distribution chart: efficiency per bucket (temperature, speed or drive mode) as vertical bars
// against the period's average. A bucket under the server's drive floor is an empty slot, so a gap
// reads as "not enough drives", not as "bad".
export function EfficiencyBars({
  kind,
  buckets,
  overall,
  minDrives,
}: {
  kind: "temp" | "speed" | "mode";
  buckets: EfficiencyBucket[];
  overall: number | null | undefined;
  minDrives?: number;
}) {
  const { distanceUnit, temperatureUnit } = usePreferences();
  const unit = effUnit(distanceUnit);
  const vals = buckets.map((b) => (b.mi_per_kwh == null ? null : effValue(b.mi_per_kwh, distanceUnit)));
  const avg = overall == null ? null : effValue(overall, distanceUnit);
  const max = scaleMax(vals, avg);
  // Bars are drawn in pixels inside a 150px plot, leaving 20px at the top for a bar's value.
  const S = 128;
  const best = vals.reduce<number>((bi, v, i) => (v != null && (vals[bi] == null || v > (vals[bi] as number)) ? i : bi), 0);
  return (
    <div data-eff-chart={kind}>
      <div className="relative" style={{ height: 150 }}>
        {avg != null && (
          <div
            data-eff-avg
            className="absolute inset-x-0 border-t border-dashed border-amber-500/70"
            style={{ bottom: (avg / max) * S }}
          >
            <span className="absolute -top-5 right-0 rounded-sm bg-neutral-900/80 px-1 text-[11px] tabular-nums text-amber-300">
              avg {avg.toFixed(2)}
            </span>
          </div>
        )}
        <div className="absolute inset-x-0 bottom-0 border-t border-neutral-700" aria-hidden />
        <ul className="absolute inset-0 flex items-end gap-1.5 sm:gap-2">
          {buckets.map((b, i) => {
            const v = vals[i];
            const label = bucketLabel(kind, b, distanceUnit, temperatureUnit);
            return (
              <li
                key={b.key}
                className="flex h-full min-w-0 flex-1 flex-col justify-end"
                aria-label={v == null ? `${label}: not enough drives (${b.drives})` : `${label}: ${v.toFixed(2)} ${unit}, ${b.drives} drives`}
              >
                {v == null ? (
                  <div data-eff-empty className="flex h-10 flex-col items-center justify-end rounded-t-md border border-dashed border-neutral-700 pb-1 text-[11px] text-neutral-500">
                    —
                  </div>
                ) : (
                  <>
                    <span className="mx-auto mb-0.5 rounded-sm bg-neutral-900/70 px-1 text-center text-xs font-semibold tabular-nums text-neutral-100">{v.toFixed(2)}</span>
                    <div
                      data-eff-bar
                      data-best={i === best ? "true" : undefined}
                      className="w-full rounded-t-md"
                      style={{
                        height: Math.max(2, (v / max) * S),
                        background: i === best ? "linear-gradient(180deg,#34d399,#059669)" : "linear-gradient(180deg,#10b981cc,#047857aa)",
                      }}
                    />
                  </>
                )}
              </li>
            );
          })}
        </ul>
      </div>
      <ul className="mt-1.5 flex gap-1.5 sm:gap-2" aria-hidden>
        {buckets.map((b) => (
          <li key={b.key} className="min-w-0 flex-1 text-center">
            <div className="truncate text-[11px] font-medium text-neutral-300">{bucketLabel(kind, b, distanceUnit, temperatureUnit)}</div>
            <div className={`text-[11px] tabular-nums ${b.mi_per_kwh == null ? "text-amber-300" : "text-neutral-500"}`}>{b.drives}</div>
          </li>
        ))}
      </ul>
      <p className="mt-1.5 text-[11px] text-neutral-500">
        {unit}; the number under each label is its drives
        {minDrives ? `. Buckets under ${minDrives} drives stay empty.` : "."}
      </p>
    </div>
  );
}
