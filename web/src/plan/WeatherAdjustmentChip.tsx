import { type TripPlan } from "../lib/api";

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
export function WeatherAdjustmentChip({ plan }: { plan: TripPlan }) {
  const adj = plan.weather_adjustment;
  if (!adj) return null;
  const rivianArrival = plan.Routes[0]?.ArrivalSoC ?? 0;
  // Only fire when WEATHER is what crossed the target line. If Rivian's
  // own plan was already below the target, the existing SoCBelowLimit
  // banner / Rivian-side warning is the right surface — this chip
  // claiming weather is to blame would be misleading. Likewise if the
  // weather correction didn't meaningfully move the number (< 2pp),
  // showing the chip just adds noise.
  if (!adj.below_target) return null;
  if (rivianArrival < adj.target_arrival_soc) return null;
  const delta = rivianArrival - adj.final_arrival_soc;
  if (delta < 2) return null;
  const reasons = (() => {
    // Pick the worst leg and describe its dominant weather factor(s).
    // Profile reasons (wheels/tires/accessories/payload) are
    // trip-wide so they slot in alongside per-leg weather.
    let worst = adj.legs[0];
    adj.legs.forEach((leg) => {
      if (leg.multiplier > worst.multiplier) worst = leg;
    });
    const out: string[] = [];
    if (worst.temp_c != null && worst.temp_c < 10) out.push(`cold (${worst.temp_c.toFixed(0)}°C)`);
    if (worst.temp_c != null && worst.temp_c > 30) out.push(`heat (${worst.temp_c.toFixed(0)}°C)`);
    if (worst.headwind_kph != null && worst.headwind_kph > 10) out.push(`${worst.headwind_kph.toFixed(0)} kph headwind`);
    if (worst.precip_mm != null && worst.precip_mm > 0.5) out.push("rain");
    if (adj.profile_reasons) out.push(...adj.profile_reasons);
    return out;
  })();
  // Title leans on the dominant signal: profile-only correction
  // reads "Your vehicle config..."; pure weather reads "Weather...";
  // both reads as a combined headline.
  const hasProfile = (adj.profile_reasons?.length ?? 0) > 0;
  const hasWeather = adj.legs.some(
    (l) =>
      (l.temp_c != null && (l.temp_c < 10 || l.temp_c > 30)) ||
      (l.headwind_kph != null && l.headwind_kph > 10) ||
      (l.precip_mm != null && l.precip_mm > 0.5),
  );
  const title = hasProfile && hasWeather
    ? "Weather + your config push arrival below target"
    : hasProfile
      ? "Your vehicle config pushes arrival below target"
      : "Weather pushes arrival below your target";
  return (
    <div className="rounded-lg border border-amber-700/50 bg-amber-950/30 p-3 text-sm">
      <div className="font-medium text-amber-200">{title}</div>
      <div className="mt-1 text-amber-300/80">
        Rivian's planner shows{" "}
        <span className="font-semibold text-amber-100">{rivianArrival.toFixed(0)}%</span>{" "}
        at the destination. Correcting for{" "}
        {reasons.length > 0 ? reasons.join(" + ") : "conditions along the route"},
        the realistic arrival is{" "}
        <span className="font-semibold text-amber-100">
          {adj.final_arrival_soc.toFixed(0)}%
        </span>{" "}
        - below your {adj.target_arrival_soc.toFixed(0)}% target. Consider
        adding a stop or padding the target.
      </div>
    </div>
  );
}

// planForTable trims a verbose plan label down to what fits the
// narrow Cost column. "Pass+ - $7/mo" -> "Pass+". Tesla's "Supercharging
// Membership" is the one outlier where the bare plan word is already
// generic enough that the network name in the Stop column carries
// the meaning - collapse to "Membership" so "Tesla Supercharger -
// X" / "$Y with Supercharging Membership" stops being a stutter.
