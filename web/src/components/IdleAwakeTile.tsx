import { Link } from "react-router-dom";
import { formatMinutes } from "../lib/minutes";
import { appPath } from "../lib/demo";
import type { DayActivity } from "../lib/api";
import { sleepSummary } from "../lib/truckHealth";

// The Now page's compact idle-awake tile. Today's value, the
// average over completed days in the period, a 7-day sparkline; links to the full
// chart on Truck (vehicle health lives there).
export function IdleAwakeTile({ days, loading }: { days: DayActivity[]; loading: boolean }) {
  const s = sleepSummary(days);
  const last = s.bars[s.bars.length - 1];
  const now = new Date();
  const todayStart = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
  const today = last && last.x >= todayStart ? last.value : null;
  const spark = s.bars.slice(-7);
  const max = Math.max(1, ...spark.map((b) => b.value));
  return (
    <Link
      to={`${appPath("/truck")}#sleep`}
      data-testid="idle-tile"
      className="block rounded-xl border border-neutral-800 bg-neutral-900/50 p-3 hover:border-neutral-700"
    >
      <div className="text-xs text-neutral-500">Idle awake</div>
      {s.daysWithData === 0 ? (
        <div className="mt-1 text-sm text-neutral-400">{loading ? "Loading…" : "Collecting data"}</div>
      ) : (
        <>
          <div className="mt-1 whitespace-nowrap text-xl font-semibold tabular-nums text-neutral-100">
            {today != null ? formatMinutes(today) : "—"}
          </div>
          <div className="mt-0.5 text-[11px] tabular-nums text-neutral-500">
            today · {s.avgDays > 0 ? `avg ${formatMinutes(s.avgIdleMinutes)}` : "no full days yet"}
          </div>
          <svg viewBox="0 0 70 18" className="mt-1.5 h-4 w-full" role="img" aria-label="Idle-awake time, last 7 days" preserveAspectRatio="none">
            {spark.map((b, i) => {
              const h = Math.max(1.5, (b.value / max) * 16);
              return <rect key={i} x={i * 10 + 1} y={18 - h} width={8} height={h} rx={1.5} fill="#818cf8" opacity={i === spark.length - 1 ? 1 : 0.55} />;
            })}
          </svg>
        </>
      )}
    </Link>
  );
}
