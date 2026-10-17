import type { EfficiencyBucket } from "../lib/api";
import { scaleMax } from "../lib/efficiency";

// One distribution chart: efficiency per bucket (temperature, speed or drive mode) as vertical bars
// against the period's average. A bucket under the server's drive floor is an empty slot, so a gap
// reads as "not enough drives", not as "bad".
export function EfficiencyBars({
  buckets,
  overall,
  unit,
  minDistance,
  distanceUnit,
}: {
  buckets: EfficiencyBucket[];
  overall: number | null | undefined;
  unit: string;
  minDistance?: number;
  distanceUnit?: string;
}) {
  const vals = buckets.map((b) => b.efficiency);
  const avg = overall ?? null;
  const max = scaleMax(vals, avg);
  // Bars are drawn in pixels inside a 150px plot, leaving 20px at the top for a bar's value.
  const S = 128;
  const best = vals.reduce<number>((bi, v, i) => (v != null && !buckets[i].not_enough_data && (vals[bi] == null || buckets[bi].not_enough_data || v > (vals[bi] as number)) ? i : bi), 0);
  return (
    <div data-eff-chart>
      <div className="relative" style={{ height: 150 }}>
        {avg != null && (
          <div data-eff-avg className="absolute inset-x-0 border-t border-dashed border-amber-500/70" style={{ bottom: (avg / max) * S }}>
            <span className="absolute -top-5 right-0 rounded-sm bg-neutral-900/80 px-1 text-[11px] tabular-nums text-amber-300">avg {avg.toFixed(2)}</span>
          </div>
        )}
        <div className="absolute inset-x-0 bottom-0 border-t border-neutral-700" aria-hidden />
        <ul className="absolute inset-0 flex items-end gap-1.5 sm:gap-2">
          {buckets.map((b, i) => {
            const v = b.efficiency;
            const thin = b.not_enough_data;
            return (
              <li
                key={b.key}
                className="flex h-full min-w-0 flex-1 flex-col justify-end"
                aria-label={
                  v == null ? `${b.label}: no drives` : `${b.label}: ${v.toFixed(2)} ${unit}, ${b.drives} drives${thin ? ", not enough distance to be reliable" : ""}`
                }
              >
                {v == null ? (
                  <div data-eff-empty className="flex h-10 flex-col items-center justify-end rounded-t-md border border-dashed border-neutral-700 pb-1 text-[11px] text-neutral-500">
                    —
                  </div>
                ) : (
                  <>
                    <span className={`mx-auto mb-0.5 rounded-sm bg-neutral-900/70 px-1 text-center text-xs font-semibold tabular-nums ${thin ? "text-neutral-400" : "text-neutral-100"}`}>{v.toFixed(2)}</span>
                    <div
                      data-eff-bar
                      data-thin={thin ? "true" : undefined}
                      data-best={i === best && !thin ? "true" : undefined}
                      className={`w-full rounded-t-md ${thin ? "border border-dashed border-emerald-500/60" : ""}`}
                      style={{
                        height: Math.max(2, (v / max) * S),
                        background: thin
                          ? "repeating-linear-gradient(135deg,#10b98133 0 6px,#10b98114 6px 12px)"
                          : i === best
                            ? "linear-gradient(180deg,#34d399,#059669)"
                            : "linear-gradient(180deg,#10b981cc,#047857aa)",
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
            <div className="truncate text-[11px] font-medium text-neutral-300" title={b.label}>{splitLabel(b.label).main}</div>
            {splitLabel(b.label).sub && <div className="truncate text-[11px] text-neutral-500">{splitLabel(b.label).sub}</div>}
            <div className={`text-[11px] tabular-nums ${b.not_enough_data ? "text-amber-300" : "text-neutral-500"}`}>{b.drives}</div>
          </li>
        ))}
      </ul>
      <p className="mt-1.5 text-[11px] text-neutral-500">
        {unit}; the number under each label is its drives
        {minDistance ? `. Hatched bars have under ${Math.round(minDistance)} ${distanceUnit ?? ""} of driving, so treat them as a hint.` : "."}
      </p>
    </div>
  );
}

// "Highway (89–113 km/h)" -> main "Highway", sub "89–113 km/h"; "32–50 °F" stays whole. Labels arrive
// ready to show in the user's units; this only lets the axis use two short lines.
function splitLabel(l: string): { main: string; sub: string } {
  const m = l.match(/^(.*?)\s*\((.*)\)\s*$/);
  return m ? { main: m[1], sub: m[2] } : { main: l.replace(/\s+°/, "°"), sub: "" };
}
