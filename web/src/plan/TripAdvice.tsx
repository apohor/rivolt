import { useEffect, useState } from "react";
import { type TripAdvice } from "../lib/api";
import { Card, Spinner } from "../components/ui";

export function TripAdviceCard({
  advice,
  loading,
  onAnalyze,
}: {
  advice?: TripAdvice;
  loading: boolean;
  onAnalyze?: () => void;
}) {
  // Collapsed-by-default once advice arrives: keeps the card a single
  // line so the map below doesn't jump when the LLM response lands.
  // User opts in to the full breakdown by tapping the "ready" row.
  const [expanded, setExpanded] = useState(false);
  // Reset when a new plan's advice replaces the prior one - the
  // user should re-opt-in to view each fresh analysis. Keyed on
  // headline because object identity isn't stable across renders.
  useEffect(() => {
    setExpanded(false);
  }, [advice?.headline]);

  return (
    <Card title="Trip analysis">
      {!loading && !advice && (
        <button
          type="button"
          onClick={onAnalyze}
          disabled={!onAnalyze}
          className="rounded-md bg-neutral-800 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-700 disabled:cursor-not-allowed disabled:opacity-40"
        >
          Analyze plan
        </button>
      )}
      {loading && !advice && (
        <div className="flex items-center gap-2 text-sm text-neutral-400">
          <Spinner />
          <span>Analyzing plan…</span>
        </div>
      )}
      {advice && (
        <>
          {/* Single toggle row at the TOP of the card — the
              right-side label flips between "Tap to view" (when
              collapsed) and "Tap to collapse" (when expanded) so the
              affordance never moves under the user's cursor. */}
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            className="flex items-center gap-2 text-sm text-left rounded-md w-full hover:bg-neutral-900/50 transition-colors py-1 -my-1 px-1"
          >
            <span className="inline-flex h-4 w-4 items-center justify-center rounded-full bg-emerald-700/40 text-emerald-300 text-[10px]">✓</span>
            <span className="text-neutral-200">Analysis ready</span>
            {advice.headline && (
              <span className="text-neutral-500 truncate">- {advice.headline}</span>
            )}
            <span className="ml-auto text-xs text-neutral-500">
              {expanded ? "Tap to collapse" : "Tap to view"}
            </span>
          </button>
          {expanded && (
            <div className="space-y-4 mt-3">
              <AdviceCostStrip cost={advice.cost_estimate} />
              <AdviceSection label="Cost" items={advice.cost} accent="emerald" />
              <AdviceSection label="Efficiency" items={advice.efficiency} accent="sky" />
              <AdviceSection label="Weather" items={advice.weather} accent="cyan" />
              <AdviceSection label="Vehicle" items={advice.vehicle} accent="amber" />
              {advice.model && (
                <p className="pt-1 border-t border-neutral-800/60 text-xs text-neutral-600">
                  {advice.model}
                </p>
              )}
            </div>
          )}
        </>
      )}
    </Card>
  );
}

// AdviceCostStrip renders the deterministic cost numbers the server
// computed (not the LLM). Always shown when there's any signal —
// purely-DCFC trips show only the DCFC line, home-energy trips show
// only the home equivalent.
export function AdviceCostStrip({ cost }: { cost: TripAdvice["cost_estimate"] }) {
  // Totals + per-network breakdown moved into each RouteCard. This
  // strip survives only as the trip-wide "you could save $X more by
  // joining every applicable plan" hint, which is a membership
  // upsell and naturally belongs in the analysis card. Render
  // nothing when nothing is actionable.
  const maxExtra = cost.dcfc_spend_user_member - cost.dcfc_spend_all_member;
  if (cost.dcfc_spend <= 0 || maxExtra < 0.5) return null;
  const fmt = (v: number) =>
    new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: cost.currency || "USD",
    }).format(v);
  return (
    <div className="rounded-md border border-amber-700/40 bg-amber-950/20 px-3 py-2 text-xs text-amber-300/80">
      You could save another{" "}
      <span className="font-semibold text-amber-100">{fmt(maxExtra)}</span> on
      this trip by joining the charging-network memberships you don't have yet
      (lowest possible cost: {fmt(cost.dcfc_spend_all_member)}).
    </div>
  );
}

// AdviceSection renders one category of LLM-written observations.
// Hidden entirely when the model returned nothing for that category
// so we don't show empty headers.
export function AdviceSection({
  label,
  items,
  accent,
}: {
  label: string;
  items: string[] | undefined;
  accent: "emerald" | "sky" | "cyan" | "amber";
}) {
  if (!items || items.length === 0) return null;
  const dot = {
    emerald: "text-emerald-500",
    sky: "text-sky-500",
    cyan: "text-cyan-500",
    amber: "text-amber-500",
  }[accent];
  return (
    <div>
      <div className="mb-1 text-xs uppercase tracking-wide text-neutral-500">{label}</div>
      <ul className="space-y-1.5 text-sm text-neutral-300">
        {items.map((ins, i) => (
          <li key={i} className="flex gap-2">
            <span className={`mt-0.5 shrink-0 ${dot}`}>·</span>
            <span>{ins}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

