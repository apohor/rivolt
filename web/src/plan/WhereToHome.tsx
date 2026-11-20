import { lazy, Suspense, useEffect, useId, useState, type ReactNode } from "react";
import type { DemoTripPreset, SavedTrip } from "../lib/api";
import { isSavedMultiday } from "../lib/api";
import { usePreferences } from "../lib/preferences";
import { multiDayFirst, tripSubtitle, tripTag } from "./tripSubtitle";

// The planner's front door (design_preview "plan_where_to"): the map with one "Where to?" entry over it, the saved trips and the sample trips under
// it. On a phone the map fills the screen and the entry and the lists are laid over it, the lists as a panel at the bottom (in the page's flow, not
// a drag sheet, so a large text size just makes the page longer); from 1024px up one panel floats at the map's left, the entry on top of the lists.
// The page owns the data and the planning: this is the layout and the little state of its own (the radar, the samples folded or not).
const PlanHomeMap = lazy(() => import("../map/PlanHomeMap").then((m) => ({ default: m.PlanHomeMap })));

// How many sample trips show before "All N".
const SAMPLES_SHOWN = 3;
// How many saved trips show before the Saved tab.
const SAVED_SHOWN = 3;

function tripMeters(t: SavedTrip): number | null {
  const p = t.plan;
  if (!p) return null;
  if (isSavedMultiday(p)) return p.total?.distance_meters ?? null;
  const m = p.Routes?.[0]?.TotalDriveDistanceMeters;
  return typeof m === "number" && m > 0 ? m : null;
}

export function WhereToHome({
  demo,
  truck,
  fromLabel,
  search,
  departure,
  departureLabel,
  saved,
  savedLoading,
  onOpenSaved,
  onShowSaved,
  samples,
  samplesLoading,
  sampleBusy,
  sampleError,
  onOpenSample,
}: {
  demo: boolean;
  truck: { lat: number; lon: number } | null;
  // "From your truck" or the place the trip starts from.
  fromLabel: string;
  // The destination search (a signed-in account); the demo plans only its samples, so there the entry filters them.
  search: ReactNode;
  // The departure picker, shown under the chips when "Leave now" is tapped.
  departure: ReactNode;
  departureLabel: string;
  saved: SavedTrip[];
  savedLoading: boolean;
  onOpenSaved: (t: SavedTrip) => void;
  onShowSaved: () => void;
  samples: DemoTripPreset[];
  samplesLoading: boolean;
  sampleBusy: string | null;
  sampleError: string | null;
  onOpenSample: (p: DemoTripPreset) => void;
}) {
  const { distanceUnit } = usePreferences();
  const [radar, setRadar] = useState(false);
  const [radarLabel, setRadarLabel] = useState("");
  const [timeOpen, setTimeOpen] = useState(false);
  const [allSamples, setAllSamples] = useState(false);
  const [filter, setFilter] = useState("");
  const timeId = useId();
  // Whether the server serves radar is asked of the map code once it loads, so the page's own chunk stays out of the map chunk's budget.
  const [radarServed, setRadarServed] = useState(false);
  useEffect(() => {
    let alive = true;
    void import("../map/radar").then((m) => alive && setRadarServed(m.radarEnabled()));
    return () => {
      alive = false;
    };
  }, []);
  const radarOn = radarServed && radarLabel !== "denied";
  const miles = (m: number) => `${Math.round(distanceUnit === "mi" ? m / 1609.344 : m / 1000)} ${distanceUnit}`;

  const q = filter.trim().toLowerCase();
  const sampleList = multiDayFirst(samples).filter((p) => !q || `${p.name} ${tripSubtitle(p.inputs)}`.toLowerCase().includes(q));
  const shownSamples = allSamples || q ? sampleList : sampleList.slice(0, SAMPLES_SHOWN);
  const savedShown = saved.slice(0, SAVED_SHOWN);

  const card = (
    <div data-where-card className="pointer-events-auto mx-3 mt-3 rounded-2xl border border-neutral-700 bg-neutral-900/95 shadow-lg lg:mx-0 lg:mt-0 lg:rounded-none lg:border-0 lg:bg-transparent lg:shadow-none">
      <div className="flex items-center gap-2 px-3 pt-2.5">
        <svg aria-hidden width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="shrink-0 text-neutral-400">
          <circle cx="11" cy="11" r="7" />
          <path d="m20 20-3.5-3.5" />
        </svg>
        <div className="min-w-0 flex-1">
          {demo ? (
            <input
              type="search"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Where to?"
              aria-label="Where to? Search the sample trips"
              className="w-full rounded-md bg-transparent px-1 py-2 text-base text-neutral-100 placeholder-neutral-400 focus:outline-hidden"
            />
          ) : (
            search
          )}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-1.5 px-3 pb-3 pt-1">
        <span data-plan-chip className="inline-flex max-w-full items-center gap-1.5">
          <span aria-hidden className="h-2 w-2 shrink-0 rounded-full bg-emerald-400" />
          <span className="min-w-0 break-words">{fromLabel}</span>
        </span>
        <button type="button" data-plan-chip aria-expanded={timeOpen} aria-controls={timeId} onClick={() => setTimeOpen((v) => !v)}>
          {departureLabel}
        </button>
      </div>
      <div id={timeId} hidden={!timeOpen} className="border-t border-neutral-800 px-3 pb-3 pt-2">
        {departure}
      </div>
    </div>
  );

  const radarChip = radarOn && (
    <div className="pointer-events-none flex justify-end px-3 pt-2 lg:absolute lg:right-3 lg:top-3 lg:p-0">
      <div className="pointer-events-auto flex min-w-0 max-w-full flex-wrap items-center justify-end gap-x-2 gap-y-1">
        {radar && radarLabel && radarLabel !== "denied" && (
          <span data-radar-label className="rounded-full border border-neutral-700 bg-neutral-900/90 px-2.5 py-1 text-xs text-neutral-300">{radarLabel}</span>
        )}
        <button
          type="button"
          aria-pressed={radar}
          onClick={() => setRadar((v) => !v)}
          className={`inline-flex items-center gap-2 rounded-full border px-3 py-1.5 text-sm ${radar ? "border-emerald-600/60 bg-emerald-950/80 text-emerald-100" : "border-neutral-700 bg-neutral-900/90 text-neutral-200"}`}
        >
          <span aria-hidden className="h-1.5 w-9 rounded-full" style={{ background: "linear-gradient(90deg,#04e9e7,#019ff4,#02fd02,#fdf802,#fd9500,#fd0000)" }} />
          Radar
        </button>
      </div>
    </div>
  );

  const lists = (
    <div data-where-lists className="pointer-events-auto rounded-t-2xl border-t border-neutral-700 bg-neutral-950 px-4 pb-4 pt-3 shadow-[0_-8px_24px_rgb(0_0_0/0.25)] lg:max-h-[calc(100dvh-16rem)] lg:overflow-y-auto lg:rounded-none lg:border-t lg:border-neutral-800 lg:bg-transparent lg:px-4 lg:shadow-none">
      {!demo && (
        <section aria-label="Saved trips">
          <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
            <h2 className="ui-label">Recent and saved</h2>
            <button type="button" onClick={onShowSaved} className="text-sm text-emerald-300 hover:text-emerald-200">
              Saved trips{saved.length > 0 ? ` (${saved.length})` : ""} ›
            </button>
          </div>
          {savedLoading ? (
            <p className="py-3 text-sm text-neutral-500">Loading…</p>
          ) : savedShown.length === 0 ? (
            <p className="py-3 text-sm text-neutral-500">Trips you save show up here, one tap from planning again.</p>
          ) : (
            <ul className="mt-1 divide-y divide-neutral-800">
              {savedShown.map((t) => {
                const m = tripMeters(t);
                return (
                  <li key={t.id}>
                    <button type="button" onClick={() => onOpenSaved(t)} className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 py-2.5 text-left">
                      <span className="min-w-0 flex-auto">
                        <span className="block text-base font-medium text-neutral-100 break-words">{t.name}</span>
                        <span className="block text-xs text-neutral-400 break-words">{tripSubtitle(t.inputs)}</span>
                      </span>
                      {m != null && <span className="shrink-0 text-xs text-neutral-400">{miles(m)}</span>}
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </section>
      )}
      {(samplesLoading || samples.length > 0 || demo) && (
        <section aria-label="Sample trips" className={demo ? "" : "mt-3 border-t border-neutral-800 pt-3"}>
          <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
            <h2 className="ui-label">Sample trips</h2>
            {!q && samples.length > SAMPLES_SHOWN && (
              <button type="button" aria-expanded={allSamples} onClick={() => setAllSamples((v) => !v)} className="text-sm text-emerald-300 hover:text-emerald-200">
                {allSamples ? "Fewer" : `All ${samples.length}`} {allSamples ? "" : "›"}
              </button>
            )}
          </div>
          {samplesLoading ? (
            <p className="py-3 text-sm text-neutral-500">Loading…</p>
          ) : shownSamples.length === 0 ? (
            <p className="py-3 text-sm text-neutral-500">{q ? "No sample trip matches that." : "No sample trips available right now."}</p>
          ) : (
            <ul className="mt-2 flex flex-wrap gap-2" data-testid="demo-presets">
              {shownSamples.map((p) => (
                <li key={p.id} className="max-w-full">
                  <button
                    type="button"
                    onClick={() => onOpenSample(p)}
                    disabled={sampleBusy != null}
                    className="max-w-full rounded-xl border border-neutral-700 bg-neutral-900 px-3 py-2 text-left text-sm hover:border-neutral-500 disabled:opacity-60"
                  >
                    <span className="block text-neutral-100 break-words">{p.name}</span>
                    <span className="block text-xs text-neutral-400">
                      {Math.round(p.summary.distance_mi)} mi
                      {tripTag(p.inputs) && <span className="ml-1.5 rounded-sm bg-emerald-900/40 px-1.5 py-0.5 text-[11px] font-medium text-emerald-200">{tripTag(p.inputs)}</span>}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          {sampleError && <p role="alert" className="mt-2 text-xs text-rose-400">{sampleError}</p>}
        </section>
      )}
    </div>
  );

  return (
    <div
      data-where-to
      className="relative flex min-h-[max(24rem,calc(100dvh-18rem))] flex-col overflow-hidden rounded-2xl border border-neutral-800 bg-neutral-900 lg:block lg:h-[calc(100dvh-11rem)] lg:min-h-[32rem]"
    >
      <Suspense fallback={<div className="absolute inset-0 animate-pulse bg-neutral-900" />}>
        <PlanHomeMap truck={truck} radar={radar && radarOn} onRadar={setRadarLabel} />
      </Suspense>
      {/* Over the map: nothing here takes a touch except the entry, the chip and the lists, so the map between them stays draggable. */}
      <div className="pointer-events-none relative z-10 flex min-h-0 flex-1 flex-col lg:absolute lg:left-3 lg:top-3 lg:block lg:w-[24.5rem] lg:flex-none">
        <div className="contents lg:pointer-events-auto lg:block lg:max-h-full lg:overflow-visible lg:rounded-2xl lg:border lg:border-neutral-700 lg:bg-neutral-900/95 lg:shadow-xl">
          {card}
          {radarChip && <div className="contents lg:hidden">{radarChip}</div>}
          <div className="min-h-24 flex-1 lg:hidden" />
          {lists}
        </div>
      </div>
      {radarChip && <div className="hidden lg:block">{radarChip}</div>}
    </div>
  );
}
