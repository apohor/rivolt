import { useQuery } from "@tanstack/react-query";
import { backend, type LeaseStatus } from "../lib/api";
import { Card } from "./ui";
import { leaseBar, planStanding } from "../lib/lease";
import { formatMoney } from "../lib/ledger";
import { usePreferences } from "../lib/preferences";

// Truck → Lease (new look): miles used against what the lease allows, as a progress bar with a
// marker for where you should be today, then the projected return and the miles-a-day budget.
// Hidden when no lease is set (the status 404s).
const KM = 1.609344;

export function LeaseCard({ vehicleID }: { vehicleID: string }) {
  const { distanceUnit } = usePreferences();
  const termsQ = useQuery({ queryKey: ["lease", "terms", vehicleID], queryFn: () => backend.leaseGet(vehicleID), staleTime: 60_000, retry: 0 });
  const statusQ = useQuery({ queryKey: ["lease", "status", vehicleID], queryFn: () => backend.leaseStatus(vehicleID), enabled: !!termsQ.data, staleTime: 60_000, retry: 0 });
  const terms = termsQ.data;
  const st = statusQ.data;
  if (!terms) return null;
  if (!st) {
    return statusQ.isLoading ? <div data-lease-state="loading" className="h-32 animate-pulse rounded-xl border border-neutral-800 bg-neutral-900/50" aria-label="Loading lease" /> : null;
  }
  return <LeaseBody st={st} allowed={terms.allowed_miles_total} currency={terms.currency} end={terms.end_date} unit={distanceUnit} />;
}

const dist = (mi: number, unit: "mi" | "km") => `${Math.round(unit === "km" ? mi * KM : mi).toLocaleString()} ${unit}`;

export function LeaseBody({ st, allowed, currency, end, unit }: { st: LeaseStatus; allowed: number; currency: string; end: string; unit: "mi" | "km" }) {
  const bar = leaseBar(st, allowed);
  const standing = planStanding(st);
  const fill = bar.tone === "over" ? "bg-rose-500" : bar.tone === "watch" ? "bg-amber-500" : "bg-emerald-500";
  const standTone = standing?.tone === "watch" ? "text-amber-300" : "text-emerald-300";
  const over = st.projected_overage_mi != null && st.projected_overage_mi > 0;
  const endLabel = new Date(`${end}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
  const standingText = standing && unit === "km" ? standing.text.replace(/([\d,]+) mi/, (_, n) => `${Math.round(Number(n.replace(/,/g, "")) * KM).toLocaleString()} km`) : standing?.text;
  return (
    <Card title="Lease" id="lease" span="full">
      <div data-lease-state="ready" className="space-y-4">
        {st.used_mi == null && (
          <p data-lease-stale role="status" className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-100/90">
            No recent odometer reading{st.odometer_at ? ` (last ${new Date(st.odometer_at).toLocaleDateString(undefined, { month: "short", day: "numeric" })})` : ""}, so miles used and the projection are paused. They come back once the truck reports in.
          </p>
        )}
        <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-1">
          <p data-lease-used className="text-2xl font-semibold tabular-nums text-neutral-50">
            {st.used_mi != null ? Math.round(unit === "km" ? st.used_mi * KM : st.used_mi).toLocaleString() : "—"}
            <span className="ml-1 text-sm font-normal text-neutral-400">of {dist(allowed, unit)}</span>
          </p>
          {standing && <p data-lease-standing className={`text-sm font-medium tabular-nums ${standTone}`}>{standingText}</p>}
        </div>

        <div>
          <div
            role="progressbar"
            aria-label="Lease miles used"
            aria-valuemin={0}
            aria-valuemax={Math.round(allowed)}
            aria-valuenow={Math.round(st.used_mi ?? 0)}
            aria-valuetext={`${dist(st.used_mi ?? 0, unit)} used of ${dist(allowed, unit)}; ${dist(st.allowed_to_date_mi, unit)} allowed to date`}
            className="relative h-3 rounded-full bg-neutral-800"
          >
            {bar.projectedPct != null && bar.projectedPct > bar.usedPct && (
              <div data-lease-projected className="absolute inset-y-0 left-0 rounded-full bg-neutral-500/40" style={{ width: `${bar.projectedPct}%` }} />
            )}
            <div data-lease-fill className={`absolute inset-y-0 left-0 rounded-full ${fill}`} style={{ width: `${bar.usedPct}%` }} />
            <div data-lease-plan className="absolute -inset-y-1 w-0.5 rounded-full bg-neutral-100" style={{ left: `${bar.planPct}%` }} title="Where the pro-rata allowance is today" />
            {bar.allowedPct < 100 && <div data-lease-limit className="absolute -inset-y-1 w-0.5 rounded-full bg-amber-400" style={{ left: `${bar.allowedPct}%` }} title="Total miles allowed" />}
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-neutral-500">
            <span><span aria-hidden className={`mr-1 inline-block h-2 w-2 rounded-sm align-middle ${fill}`} />Used</span>
            <span><span aria-hidden className="mr-1 inline-block h-2.5 w-0.5 bg-neutral-100 align-middle" />Allowed to date · {dist(st.allowed_to_date_mi, unit)}</span>
            {bar.allowedPct < 100 && <span data-lease-legend-limit><span aria-hidden className="mr-1 inline-block h-2.5 w-0.5 bg-amber-400 align-middle" />Total allowed · {dist(allowed, unit)}</span>}
            {bar.projectedPct != null && <span><span aria-hidden className="mr-1 inline-block h-2 w-2 rounded-sm bg-neutral-500/50 align-middle" />Projected at return</span>}
          </div>
        </div>

        <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-sm sm:grid-cols-3">
          <div>
            <dt className="ui-label">Returns</dt>
            <dd className="font-semibold tabular-nums text-neutral-100">{endLabel}</dd>
            <dd className="text-xs text-neutral-500">{st.days_left.toLocaleString()} days left</dd>
          </div>
          <div>
            <dt className="ui-label">Projected at return</dt>
            <dd data-lease-projected-total className="font-semibold tabular-nums text-neutral-100">
              {st.projected_total_mi != null ? dist(st.projected_total_mi, unit) : "—"}
              <span className="ml-1 text-xs font-normal text-neutral-400">of lease {unit === "km" ? "kilometers" : "miles"}</span>
            </dd>
            {st.projected_return_odometer_mi != null && (
              <dd data-lease-projected-odo className="text-xs tabular-nums text-neutral-500">odometer ~{dist(st.projected_return_odometer_mi, unit)}</dd>
            )}
            {st.pace_mi_per_day != null && (
              <dd className="text-xs text-neutral-500">at {dist(st.pace_mi_per_day, unit)} a day{st.pace_basis === "last_90_days" ? " (last 90 days)" : st.pace_basis === "lease_to_date" ? " (since the lease began)" : ""}</dd>
            )}
          </div>
          <div>
            <dt className="ui-label">Overage</dt>
            {st.projected_overage_mi == null ? (
              <dd data-lease-overage className="font-semibold text-neutral-400">—</dd>
            ) : over ? (
              <>
                <dd data-lease-overage className="font-semibold tabular-nums text-rose-300">~{formatMoney(st.projected_overage_cost ?? 0, currency)}</dd>
                <dd className="text-xs text-neutral-500">{dist(st.projected_overage_mi as number, unit)} over</dd>
              </>
            ) : (
              <dd data-lease-overage className="font-semibold text-emerald-300">None projected</dd>
            )}
          </div>
          <div className="col-span-2 sm:col-span-3">
            <dt className="ui-label">{unit === "km" ? "Kilometers" : "Miles"} a day to stay within the allowance</dt>
            <dd data-lease-budget className="text-lg font-semibold tabular-nums text-neutral-100">
              {st.miles_per_day_budget != null ? `${(unit === "km" ? st.miles_per_day_budget * KM : st.miles_per_day_budget).toFixed(1)} ${unit}` : "—"}
              <span className="ml-2 text-xs font-normal text-neutral-500">{st.miles_per_day_budget === 0 ? "already over the allowance" : "from today to the end of the lease"}</span>
            </dd>
          </div>
        </dl>
      </div>
    </Card>
  );
}
