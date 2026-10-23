import { formatDuration } from "./format";
import { type TripCostEstimate, type TripRoute } from "../lib/api";
import { comparableSpend, fmtRange, spendView } from "../lib/planCost";


// planForTable trims a verbose plan label down to what fits the
// narrow Cost column. "Pass+ - $7/mo" -> "Pass+". Tesla's "Supercharging
// Membership" is the one outlier where the bare plan word is already
// generic enough that the network name in the Stop column carries
// the meaning - collapse to "Membership" so "Tesla Supercharger -
// X" / "$Y with Supercharging Membership" stops being a stutter.
export function planForTable(plan: string): string {
  const short = plan.replace(/\s*[-,].*/, "").trim();
  return short === "Supercharging Membership" ? "Membership" : short;
}

// routeLabel picks the route's name. Simple two-tier scheme:
// Rivian's top pick is "Recommended"; everything after is
// "Alternative N". The descriptive variants ("Faster by 22 min",
// etc.) competed for screen space with the cost line and lost -
// the user can see Distance / Total time / Arrival in the stat
// grid right below the title.
// RouteChooser: the planner's compact route picker (Recommended and
// alternatives as selectable chips with cost, time and stops).
export function RouteChooser({
  routes,
  costs,
  active,
  onChange,
}: {
  routes: TripRoute[];
  costs?: TripCostEstimate[];
  active: number;
  onChange: (i: number) => void;
}) {
  const money = (v: number, cur?: string) =>
    new Intl.NumberFormat(undefined, { style: "currency", currency: cur || "USD", maximumFractionDigits: 0 }).format(v);
  // The chip's cost: the range, or nothing when there is no price (the route card says why).
  const chipSpend = (c: TripCostEstimate | undefined, fmt: (v: number, cur?: string) => string) => {
    const v = spendView(c);
    return v.kind === "range" ? `${fmtRange(v.low, v.high, (x) => fmt(x, c?.currency))} · ` : "";
  };
  return (
    <div role="radiogroup" aria-label="Route options" data-plan-routes className="mb-3 flex gap-2 overflow-x-auto pb-1">
      {routes.map((r, i) => {
        const c = costs?.[i];
        const diff = i === active || i === 0 ? [] : routeDiff(r, c, routes[0], costs?.[0]);
        return (
          <button
            key={i}
            type="button"
            role="radio"
            aria-checked={i === active}
            onClick={() => onChange(i)}
            className={`min-w-32 shrink-0 rounded-lg border px-3 py-2 text-left text-xs transition-colors ${
              i === active ? "border-emerald-600/60 bg-emerald-600/10" : "border-neutral-800 hover:border-neutral-600"
            }`}
          >
            <span className="block text-sm font-medium text-neutral-100">{routeLabel(i, routes)}</span>
            <span className="mt-0.5 block tabular-nums text-neutral-400">
              {chipSpend(c, money)}
              {formatDuration(r.TotalTripDurationSec)} · {countChargingStops(r)} {countChargingStops(r) === 1 ? "stop" : "stops"}
            </span>
            {diff.length > 0 && <span className="mt-0.5 block text-[11px] text-neutral-500">{diff.slice(0, 2).join(" · ")}</span>}
          </button>
        );
      })}
    </div>
  );
}

export function routeLabel(index: number, allRoutes: TripRoute[]): string {
  if (allRoutes.length <= 1) return "Recommended";
  if (index === 0) return "Recommended";
  return `Alternative ${index}`;
}

// countChargingStops applies the same waypoint-type filter the
// stops table uses: drop origin / destination / waypoint / other.
export function countChargingStops(r: TripRoute): number {
  return r.Waypoints.filter((w) => {
    const t = w.WaypointType.toLowerCase();
    return t !== "origin" && t !== "destination" && t !== "waypoint" && t !== "other";
  }).length;
}

// routeDiff builds the "saves $12 · 18 min slower · 1 fewer stop"
// summary line shown under each alternative's title. Each dimension
// has a small noise threshold so a trivially-different route doesn't
// pretend to be meaningfully different. Currency-format the cost
// delta with the route's own currency so EU / GB users see € / £.
export function routeDiff(
  route: TripRoute,
  cost: TripCostEstimate | undefined,
  primary: TripRoute,
  primaryCost: TripCostEstimate | undefined,
): string[] {
  const parts: string[] = [];
  const fmtMoney = (v: number) =>
    new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: cost?.currency || primaryCost?.currency || "USD",
      maximumFractionDigits: 0,
    }).format(v);

  // Only when both routes' fast-charging cost is a complete estimate: a route with an unpriced stop can't be called cheaper.
  const spendA = comparableSpend(primaryCost);
  const spendB = comparableSpend(cost);
  const dCost = spendA != null && spendB != null ? spendA - spendB : 0;
  if (Math.abs(dCost) >= 0.5) {
    parts.push(dCost > 0 ? `saves ${fmtMoney(dCost)}` : `+${fmtMoney(Math.abs(dCost))}`);
  }
  const dTime = primary.TotalTripDurationSec - route.TotalTripDurationSec;
  if (Math.abs(dTime) >= 120) {
    parts.push(
      dTime > 0 ? `${formatDuration(dTime)} faster` : `${formatDuration(-dTime)} slower`,
    );
  }
  const dStops = countChargingStops(primary) - countChargingStops(route);
  if (dStops !== 0) {
    parts.push(
      dStops > 0
        ? `${dStops} fewer stop${dStops === 1 ? "" : "s"}`
        : `${-dStops} more stop${dStops === -1 ? "" : "s"}`,
    );
  }
  const dArr = route.ArrivalSoC - primary.ArrivalSoC;
  if (Math.abs(dArr) >= 2) {
    parts.push(
      dArr > 0
        ? `arrives ${dArr.toFixed(0)}% higher`
        : `arrives ${Math.abs(dArr).toFixed(0)}% lower`,
    );
  }
  return parts;
}

