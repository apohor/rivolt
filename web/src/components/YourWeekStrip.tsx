import { useMemo } from "react";
import type { Charge, Drive } from "../lib/api";
import { formatMoney } from "../lib/ledger";
import { changeLine, yourWeek } from "../lib/yourWeek";

// The "Your week" strip at the top of Now: this week's miles, energy added and charging cost, and how the miles compare with last week so
// far. The same lists the tiles below use (nothing new is read); hidden until something is recorded this week. Cost is an estimate unless
// every priced charge was stated by Rivian or the owner, and says so.
export function YourWeekStrip({ drives, charges, now }: { drives: Drive[]; charges: Charge[]; now?: Date }) {
  const w = useMemo(() => yourWeek(drives, charges, now), [drives, charges, now]);
  if (!w) return null;
  const { week } = w;
  const change = changeLine(w.milesChangePct);
  return (
    <section aria-label="Your week" data-your-week className="rounded-xl border border-neutral-800 bg-neutral-900/50 px-4 py-3">
      <div className="text-xs font-medium uppercase tracking-wide text-neutral-400">Your week</div>
      <div className="mt-1 flex flex-wrap items-baseline gap-x-5 gap-y-1 text-sm text-neutral-200">
        <span data-week-miles>
          <span className="text-lg font-semibold tabular-nums text-neutral-50">{Math.round(week.miles)}</span> mi
        </span>
        <span data-week-kwh>
          <span className="text-lg font-semibold tabular-nums text-neutral-50">{Math.round(week.kWh)}</span> kWh added
        </span>
        {week.cost > 0 && (
          <span data-week-cost>
            <span className="text-lg font-semibold tabular-nums text-neutral-50">{formatMoney(week.cost, week.currency)}</span>
            {week.costEstimated ? " est." : ""} charging
          </span>
        )}
      </div>
      {change && <div data-week-change className="mt-1 text-xs text-neutral-400">{change}</div>}
    </section>
  );
}
