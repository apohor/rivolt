import { useEffect, useState } from "react";
import { useUiNext } from "../lib/uiNext";
import { inDemo } from "../lib/demo";
import { type TripAdvice, type TripPlan } from "../lib/api";
import { Card, ErrorBox } from "../components/ui";
import { StopAdder, StopPreviewer } from "./tripTypes";
import { WeatherAdjustmentChip } from "./WeatherAdjustmentChip";
import { RouteChooser } from "./RouteChooser";
import { RouteCard } from "./RouteCard";
import { TripAdviceCard } from "./TripAdvice";

export function TripPlanResult({
  plan,
  originLabel,
  destLabel,
  advice,
  adviceLoading,
  onAnalyze,
  onAddStop,
  onAddOvernight,
  onPreviewStop,
  departureAt,
}: {
  plan: TripPlan;
  originLabel: string;
  destLabel: string;
  advice?: TripAdvice;
  adviceLoading?: boolean;
  onAnalyze?: () => void;
  onAddStop?: StopAdder;
  onAddOvernight?: StopAdder;
  onPreviewStop?: StopPreviewer;
  departureAt?: string;
}) {
  const nextUi = useUiNext();
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
  return (
    <div className="space-y-4" data-plan-contents={nextUi ? "" : undefined}>
      {/* AI analysis costs money: never offered in the public demo. */}
      {!inDemo() && <TripAdviceCard advice={advice} loading={adviceLoading ?? false} onAnalyze={onAnalyze} />}
      <WeatherAdjustmentChip plan={plan} />
      {nextUi ? (
        (() => {
          const i = Math.min(active, plan.Routes.length - 1);
          const route = plan.Routes[i];
          return (
            <RouteCard
              key={i}
              route={route}
              index={i}
              forceNext
              allRoutes={plan.Routes}
              originLabel={originLabel}
              destLabel={destLabel}
              onAddStop={onAddStop}
              onAddOvernight={onAddOvernight}
              onPreviewStop={onPreviewStop}
              departureAt={departureAt}
              cost={plan.costs?.[i]}
              primaryCost={plan.costs?.[0]}
              adjustment={i === 0 ? plan.weather_adjustment : undefined}
              chooser={
                plan.Routes.length > 1 ? (
                  <RouteChooser routes={plan.Routes} costs={plan.costs} active={i} onChange={setActive} />
                ) : undefined
              }
            />
          );
        })()
      ) : (
      plan.Routes.map((route, i) => (
        <RouteCard
          key={i}
          route={route}
          index={i}
          allRoutes={plan.Routes}
          originLabel={originLabel}
          destLabel={destLabel}
          onAddStop={onAddStop}
          onAddOvernight={onAddOvernight}
          onPreviewStop={onPreviewStop}
          departureAt={departureAt}
          defaultCollapsed={i > 0}
          cost={plan.costs?.[i]}
          primaryCost={plan.costs?.[0]}
          adjustment={i === 0 ? plan.weather_adjustment : undefined}
        />
      ))
      )}
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
