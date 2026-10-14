import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { bars, EXCLUDED_LABEL, formatHours, formatPct, view } from "../lib/drain";

// Parked-drain card for the Truck tab (docs: drain-contract.md). The
// headline is %/day with awake hours as the explanation. It says what
// happened, never why, and when the data is thin it says that instead
// of guessing. Hidden when the endpoint is absent (404).
export default function DrainCard({ vehicleID }: { vehicleID: string }) {
  const [win, setWin] = useState<"7d" | "30d">("7d");
  const q = useQuery({
    queryKey: ["vehicle", "drain", vehicleID],
    queryFn: () => backend.vehicleDrain(vehicleID),
    staleTime: 5 * 60_000,
    retry: 0,
  });
  if (q.isLoading) return <div className="h-32 animate-pulse rounded-xl border border-neutral-800 bg-neutral-900/50" />;
  if (!q.data) return null; // 404 or error: no card
  const d = q.data;
  const v = view(d.windows[win], win);
  const other = win === "7d" ? "30d" : "7d";
  const ov = view(d.windows[other], other);
  const { bars: bs, max } = bars(d.daily);

  return (
    <section aria-label="Parked drain" className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-4">
      <div className="flex items-start justify-between gap-3">
        <h2 className="text-base font-semibold text-neutral-100">Parked drain</h2>
        <div role="tablist" aria-label="Window" className="inline-flex rounded-lg border border-neutral-800 p-0.5 text-xs">
          {(["7d", "30d"] as const).map((k) => (
            <button
              key={k}
              type="button"
              role="tab"
              aria-selected={win === k}
              onClick={() => setWin(k)}
              className={`rounded-md px-2.5 py-1 ${win === k ? "bg-neutral-800 text-neutral-100" : "text-neutral-400 hover:text-neutral-100"}`}
            >
              {k === "7d" ? "7 days" : "30 days"}
            </button>
          ))}
        </div>
      </div>

      {v.kind === "ok" ? (
        <>
          <div className="mt-2 flex flex-wrap items-baseline gap-x-3">
            <span className="text-4xl font-semibold tabular-nums text-neutral-100">
              {formatPct(v.pctPerDay)}
              <span className="ml-0.5 text-xl font-normal text-neutral-500">% / day</span>
            </span>
            {v.kwhPerDay != null && <span className="text-sm tabular-nums text-neutral-400">≈ {v.kwhPerDay.toFixed(1)} kWh / day</span>}
          </div>
          <p className="mt-2 text-sm text-neutral-300">
            {v.idleAwakeHours != null ? `Awake ${formatHours(v.idleAwakeHours)} a day while parked. ` : ""}
            {v.sleeps} {v.sleeps === 1 ? "sleep" : "sleeps"}
            {v.quickRewakes > 0 ? `, ${v.quickRewakes} under a minute` : ""}
            {v.medianSleepSeconds != null ? `, median ${formatHours(v.medianSleepSeconds / 3600)}` : ""}.
          </p>
          <p className="mt-1 text-xs text-neutral-500">
            Based on {v.daysUsed} {v.daysUsed === 1 ? "day" : "days"}
            {v.daysExcluded > 0 ? ` (${v.daysExcluded} left out: too little parked data)` : ""}.
            {ov.kind === "ok" ? ` ${other === "7d" ? "7 days" : "30 days"}: ${formatPct(ov.pctPerDay)}% / day.` : ""}
          </p>
        </>
      ) : (
        <div className="mt-3">
          <p className="text-sm text-neutral-300">Not enough parked time yet to measure drain.</p>
          <p className="mt-1 text-xs text-neutral-500">
            {formatHours(v.haveHours)} of parked data so far; the {win === "7d" ? "7-day" : "30-day"} figure needs{" "}
            {v.needHours} h. It appears on its own as data comes in.
          </p>
        </div>
      )}

      {d.daily.length > 0 && (
        <figure className="mt-4">
          <svg
            viewBox={`0 0 ${bs.length * 10} 40`}
            role="img"
            aria-label={`Daily parked drain, last ${bs.length} days. Left-out days are outlined.`}
            className="h-16 w-full text-emerald-500"
            preserveAspectRatio="none"
          >
            {bs.map((b, i) =>
              b.value == null ? (
                <rect key={b.day} x={i * 10 + 1} y={30} width={8} height={9} rx={1} fill="none" stroke="currentColor" strokeOpacity={0.35} strokeWidth={1} vectorEffect="non-scaling-stroke">
                  <title>{`${b.day}: ${b.excluded ? EXCLUDED_LABEL[b.excluded] : "no data"}`}</title>
                </rect>
              ) : (
                <rect key={b.day} x={i * 10 + 1} y={39 - Math.max(1, b.h * 38)} width={8} height={Math.max(1, b.h * 38)} rx={1} fill="currentColor" fillOpacity={b.gained != null ? 0.5 : 1}>
                  <title>
                    {b.gained != null
                      ? `${b.day}: battery rose ${formatPct(b.gained)}% while parked (measurement noise or a top-up)`
                      : `${b.day}: ${formatPct(b.value)}% / day`}
                  </title>
                </rect>
              ),
            )}
          </svg>
          <figcaption className="mt-1 text-[11px] text-neutral-500">
            Daily drain, % per day (top of chart ≈ {formatPct(max)}%). Outlined days are left out.
          </figcaption>
        </figure>
      )}

      <p className="mt-3 text-[11px] text-neutral-500">
        Battery lost while parked and not charging. This shows what happened, not why.
      </p>
    </section>
  );
}
