import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend, type DrainSplit } from "../lib/api";
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
  // A cold first load can take 3 to 6 s: show the card's own title over
  // pulsing lines (never a bare bordered box, which read as an empty card),
  // so the slot doesn't look missing. 404 or an error removes it.
  if (q.isLoading) {
    return (
      <section
        aria-label="Loading parked drain"
        aria-busy="true"
        data-testid="drain-skeleton"
        className="rounded-xl border border-neutral-800 bg-neutral-900/50 p-4"
      >
        <h2 className="text-base font-semibold text-neutral-100">Parked drain</h2>
        <div className="mt-3 space-y-2 animate-pulse" aria-hidden>
          <div className="h-8 w-40 rounded-sm bg-neutral-800/70" />
          <div className="h-3 w-3/4 rounded-sm bg-neutral-800/60" />
          <div className="h-12 w-full rounded-sm bg-neutral-800/50" />
        </div>
      </section>
    );
  }
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
            Over {win === "7d" ? "7" : "30"} days: {v.sleeps} {v.sleeps === 1 ? "sleep" : "sleeps"}
            {v.quickRewakes > 0 ? `, ${v.quickRewakes} under a minute` : ""}
            {v.medianSleepSeconds != null ? `, median ${formatHours(v.medianSleepSeconds / 3600)}` : ""}. Time spent awake
            while parked is in the Sleep card below.
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

      {d.windows[win].split && <DrainSplitView split={d.windows[win].split as DrainSplit} win={win} />}

      {d.daily.length > 0 && (
        <figure className="mt-4">
          <svg
            viewBox={`0 0 ${bs.length * 10} 40`}
            role="img"
            aria-label={`Daily parked drain, last ${bs.length} days. Left-out days are outlined.`}
            className="h-16 w-full text-emerald-500"
            preserveAspectRatio="none"
          >
            <line x1={0} x2={bs.length * 10} y1={1} y2={1} stroke="currentColor" strokeOpacity={0.25} strokeDasharray="3 3" vectorEffect="non-scaling-stroke" />
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
            Daily drain, % per day. Dashed line = {formatPct(max)}%. Outlined days are left out.
          </figcaption>
        </figure>
      )}

      <p className="mt-3 text-[11px] text-neutral-500">
        Battery lost while parked and not charging. This shows what happened, not why.
      </p>
    </section>
  );
}

// When the parked loss happened: a sentence and a two-part bar (awake against the rest). Always an estimate (the battery
// reports whole percents). It says when, not why. Nothing is shown when the power state can't be trusted (the R2 today).
function DrainSplitView({ split, win }: { split: DrainSplit; win: "7d" | "30d" }) {
  const span = win === "7d" ? "this week" : "over 30 days";
  if (!split.sufficient) {
    if (split.reason === "not_enough_data") {
      return (
        <p data-drain-split="sparse" className="mt-3 text-xs text-neutral-500">
          Where the loss happened (asleep or awake) needs about two days of parked history with a few points lost.
        </p>
      );
    }
    if (split.reason === "power_state_gaps") {
      return <p data-drain-split="gaps" className="mt-3 text-xs text-neutral-500">The power state wasn&apos;t recorded often enough to say when the loss happened.</p>;
    }
    return null;
  }
  const asleep = split.asleep_points_lost ?? 0;
  const awake = split.awake_points_lost ?? 0;
  const total = asleep + awake;
  const share = split.awake_share_of_loss ?? 0;
  if (total <= 0) return null;
  return (
    <div data-drain-split="ok" className="mt-3 space-y-1.5">
      <p className="text-sm text-neutral-300">
        Of the {Math.round(total)}% your truck lost while parked {span}, about {Math.round(awake)}% was while it was awake
        {split.awake_hours != null ? ` (${formatHours(split.awake_hours)})` : ""}.
      </p>
      <div role="img" aria-label={`${Math.round(share * 100)}% of the loss was while awake, ${Math.round((1 - share) * 100)}% while asleep`} className="flex h-2 w-full overflow-hidden rounded-full bg-neutral-800">
        {awake > 0 && <div className="h-full bg-amber-400" style={{ width: `${share * 100}%` }} />}
        {asleep > 0 && <div className="h-full bg-emerald-500" style={{ width: `${(1 - share) * 100}%` }} />}
      </div>
      <p className="flex flex-wrap gap-x-3 text-[11px] text-neutral-500">
        {awake > 0 && <span><span aria-hidden className="mr-1 inline-block h-2 w-2 rounded-full bg-amber-400" />Awake</span>}
        {asleep > 0 && <span><span aria-hidden className="mr-1 inline-block h-2 w-2 rounded-full bg-emerald-500" />Asleep</span>}
        <span>Estimate: the battery reports whole percents.</span>
      </p>
    </div>
  );
}
