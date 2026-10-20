import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { type TripAdvice } from "../lib/api";
import { Card, Spinner } from "../components/ui";
import { AI_SETTINGS_PATH } from "../lib/aiConsent";

export function TripAdviceCard({
  advice,
  loading,
  onAnalyze,
  aiOff = false,
  error,
}: {
  advice?: TripAdvice;
  loading: boolean;
  onAnalyze?: () => void;
  // The user has not turned AI on (Settings), so there is nothing to tap yet.
  aiOff?: boolean;
  // A failed request that is not "AI is off": said in a plain sentence.
  error?: string | null;
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
      {!loading && !advice && aiOff && (
        <p className="text-sm text-neutral-300" data-ai-off>
          AI advice is off.{" "}
          <Link to={AI_SETTINGS_PATH} className="text-emerald-300 underline">
            Turn on AI advice in Settings
          </Link>{" "}
          to use it. Nothing is sent until you tap Get AI advice on a trip.
        </p>
      )}
      {!loading && !advice && !aiOff && (
        <div className="space-y-2">
          <p className="text-xs text-neutral-400" data-ai-disclosure>
            Nothing is sent until you tap. AI advice sends charger names, your locations rounded to about 11 km, your tire pressures, your home
            electricity rate and the weather along the route to an AI provider. It never sends the text you type.
          </p>
          <button
            type="button"
            onClick={onAnalyze}
            disabled={!onAnalyze}
            className="rounded-md bg-neutral-800 px-3 py-1.5 text-sm text-neutral-200 hover:bg-neutral-700 disabled:cursor-not-allowed disabled:opacity-40"
          >
            Get AI advice
          </button>
          {error && (
            <p role="alert" className="text-xs text-rose-400">
              Couldn&apos;t get AI advice just now. Try again in a moment.
            </p>
          )}
        </div>
      )}
      {loading && !advice && (
        <div className="flex items-center gap-2 text-sm text-neutral-400">
          <Spinner />
          <span>Getting AI advice…</span>
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

