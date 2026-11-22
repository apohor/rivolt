import "../styles/next-plan.css";
import { useEffect, useState } from "react";
import { inDemo } from "../lib/demo";
import { type TripAdvice, type TripPlan } from "../lib/api";
import { Card, ErrorBox } from "../components/ui";
import { OvernightAdder, StopAdder, StopPreviewer } from "./tripTypes";
import { WeatherAdjustmentChip } from "./WeatherAdjustmentChip";
import { WeatherCredit } from "../components/WeatherCredit";
import { RouteChooser } from "./RouteChooser";
import { RouteCard } from "./RouteCard";
import { OvernightSuggestions } from "./OvernightSuggestions";
import { TripAdviceCard } from "./TripAdvice";
import { FarFutureNote } from "./FarFutureNote";

export function TripPlanResult({
  plan,
  originLabel,
  destLabel,
  advice,
  adviceLoading,
  onAnalyze,
  aiEnabled = false,
  aiOff = false,
  adviceError = null,
  onAddStop,
  onAddOvernight,
  onPreviewStop,
  departureAt,
  adviceLast = false,
  afterItinerary,
}: {
  plan: TripPlan;
  originLabel: string;
  destLabel: string;
  advice?: TripAdvice;
  adviceLoading?: boolean;
  onAnalyze?: () => void;
  // Whether the server has an AI provider; without one there is no advice to offer.
  aiEnabled?: boolean;
  // The user has not turned AI on in Settings, or an AI call just said so.
  aiOff?: boolean;
  adviceError?: string | null;
  onAddStop?: StopAdder;
  onAddOvernight?: OvernightAdder;
  onPreviewStop?: StopPreviewer;
  departureAt?: string;
  // plan_v2: the trip analysis follows the summary and itinerary instead of leading them.
  adviceLast?: boolean;
  // plan_v2: Save trip, shown right under the itinerary (before the elevation profile).
  afterItinerary?: React.ReactNode;
}) {
  const [active, setActive] = useState(0);
  const planKey = plan.Routes.length;
  useEffect(() => setActive(0), [plan, planKey]);
  if (plan.Routes.length === 0) {
    return (
      <Card title="Result">
        <p className="text-sm text-neutral-400">
          Status: <span className="font-mono">{plan.Status || "—"}</span>. No
          routes returned. {plan.SoCBelowLimit && "(SoC below the configured limit.)"}
          {!plan.ChargeStationsAvailable && " (No reachable charging stations along the corridor.)"}
        </p>
      </Card>
    );
  }
  // AI analysis costs money: never offered in the public demo.
  const adviceCard = !inDemo() && (aiEnabled || !!advice) && <TripAdviceCard advice={advice} loading={adviceLoading ?? false} onAnalyze={aiEnabled ? onAnalyze : undefined} aiOff={aiOff} error={adviceError} />;
  return (
    <div className="space-y-4" data-plan-contents="">
      {!adviceLast && adviceCard}
      <FarFutureNote show={plan.far_future} />
      <WeatherAdjustmentChip plan={plan} />
      {(() => {
          const i = Math.min(active, plan.Routes.length - 1);
          const route = plan.Routes[i];
          return (
            <>
            <RouteCard
              key={i}
              route={route}
              index={i}
              forceNext
              afterItinerary={afterItinerary}
              allRoutes={plan.Routes}
              originLabel={originLabel}
              destLabel={destLabel}
              onAddStop={onAddStop}
              onAddOvernight={onAddOvernight}
              onPreviewStop={onPreviewStop}
              departureAt={departureAt}
              cost={plan.costs?.[i]}
              primaryCost={plan.costs?.[0]}
              elevation={i === 0 ? plan.elevation_profile : undefined}
              adjustment={i === 0 ? plan.weather_adjustment : undefined}
              credit={i === 0 && plan.weather_adjustment ? <WeatherCredit className="mt-3 text-xs text-neutral-500" /> : undefined}
              chooser={
                plan.Routes.length > 1 ? (
                  <RouteChooser routes={plan.Routes} costs={plan.costs} active={i} onChange={setActive} />
                ) : undefined
              }
            />
            <OvernightSuggestions route={route} day={1} onUse={onAddOvernight} />
            </>
          );
        })()}
      {adviceLast && adviceCard}
      {plan.SoCBelowLimit && (
        <ErrorBox
          title="SoC below limit"
          detail="Rivian flagged this plan as crossing the configured low-SoC threshold."
        />
      )}
    </div>
  );
}

// WeatherAdjustmentChip renders a callout when the rivolt-side
// weather correction predicts the corrected arrival SoC will fall
// below the user's target floor. Rivian's planner doesn't see
// temperature / wind / precip, so cold days and headwind days
// silently turn comfortable plans into stranded ones; this chip is
// the user's warning that they should add a stop or pad the target.
//
// Renders nothing when there's no adjustment (toggle off, fetch
// failed on every leg), or when the adjustment confirms the plan is
// still above target (cold day with extra margin).
