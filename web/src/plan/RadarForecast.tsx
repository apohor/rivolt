import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend, type TripWeatherAdjustment } from "../lib/api";
import { planState } from "../lib/plan";
import { ProSheet, ProSheetPreload } from "../components/ProSheetLazy";

// The forecast radar's two faces (design key radar_forecast). A Pro account's stops carry "Rain likely at arrival" / "Snow likely at arrival"
// (legWeather.ts); a free account gets one quiet line at the plan summary from radar_teaser, with the way to the Pro sheet and a dismiss.

// True when any leg of the adjustment carries a rain or snow forecast tag (the credit shows with it).
export const hasForecastTag = (adj: TripWeatherAdjustment | undefined) => !!adj?.legs.some((l) => l.radar?.level === "rain" || l.radar?.level === "snow");

// "Rain or snow is likely at 2 stops on this route. The Pro tier shows which." Never shown for zero wet legs: a teaser must not read as "dry".
export function teaserText(wetLegs: number): string {
  return `Rain or snow is likely at ${wetLegs === 1 ? "1 stop" : `${wetLegs} stops`} on this route. The Pro tier shows which.`;
}

// Plans whose teaser was dismissed, for this page's life (nothing is stored: the key is made of the route's own places).
const dismissed = new Set<string>();

export function RadarTeaser({ teaser, planKey }: { teaser: TripWeatherAdjustment["radar_teaser"]; planKey: string }) {
  const me = useQuery({ queryKey: ["auth", "me"], queryFn: () => backend.whoami(), staleTime: 5 * 60_000 });
  const state = planState(me.data);
  const [open, setOpen] = useState(false);
  const [gone, setGone] = useState(() => dismissed.has(planKey));
  if (!teaser || !(teaser.wet_legs > 0) || state.kind === "off" || state.kind === "pro" || gone) return null;
  return (
    <div role="note" data-radar-teaser className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-neutral-300">
      <p>{teaserText(teaser.wet_legs)}</p>
      <ProSheetPreload />
      <button type="button" className="underline underline-offset-2 hover:text-neutral-100" onClick={() => setOpen(true)}>See Pro</button>
      <button
        type="button"
        aria-label="Dismiss"
        className="text-neutral-500 hover:text-neutral-300"
        onClick={() => { dismissed.add(planKey); setGone(true); }}
      >
        Dismiss
      </button>
      {open && <ProSheet from="planner" state={state} onClose={() => setOpen(false)} />}
    </div>
  );
}

// The forecast's credit, once under the itinerary where its tags show.
export const RadarCredit = () => (
  <p data-testid="radar-credit" className="mt-2 text-[11px] text-neutral-600">Rain and snow likely at arrival: NOAA RRFS</p>
);
