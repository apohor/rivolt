import { useId, useState } from "react";
import type { GeocodeResult, SavedTrip } from "../lib/api";
import { isSavedMultiday } from "../lib/api";
import { usePreferences } from "../lib/preferences";
import { tripSubtitle } from "./tripSubtitle";
import { useGeocode } from "./useGeocode";

// The planner's front door (design_preview "plan_where_to"): the "Where to?" field first, the truck's range, then the trips already made.
// No map here: the map comes with a route. One column, so a phone and a desktop read the same, at most 720 px wide on a big screen.
// Typing swaps everything under the field for the matching places and plans. The page owns the data and the planning; this is the
// layout and the little state of its own (the text typed, the departure picker open or not).

// How many saved trips show before the Saved tab.
const SAVED_SHOWN = 4;

function tripMeters(t: SavedTrip): number | null {
  const p = t.plan;
  if (!p) return null;
  if (isSavedMultiday(p)) return p.total?.distance_meters ?? null;
  const m = p.Routes?.[0]?.TotalDriveDistanceMeters;
  return typeof m === "number" && m > 0 ? m : null;
}

const area = (r: GeocodeResult) => [r.admin1, r.country].filter(Boolean).join(", ");

function ShareIcon() {
  return (
    <svg aria-hidden width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="shrink-0">
      <path d="M12 3v12" />
      <path d="m7 8 5-5 5 5" />
      <path d="M5 13v6a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-6" />
    </svg>
  );
}

// One saved trip: tap the name to open it (a phone), Open and Share as buttons (a desktop). Share is its own 44 px target, away from anything destructive.
function TripRow({ t, onOpen, onShare, meters }: { t: SavedTrip; onOpen: () => void; onShare?: () => void; meters: string | null }) {
  const text = (
    <>
      <span className="block break-words text-base font-medium text-neutral-100">{t.name}</span>
      <span className="block break-words text-xs text-neutral-400">{tripSubtitle(t.inputs)}</span>
    </>
  );
  return (
    <li data-trip-row className="flex flex-wrap items-center gap-x-1">
      <button type="button" onClick={onOpen} className="min-h-11 min-w-0 flex-[1_1_10rem] py-2 text-left lg:hidden">
        {text}
      </button>
      <div className="hidden min-w-0 flex-[1_1_10rem] py-2 lg:block">{text}</div>
      {meters && <span className="shrink-0 text-xs text-neutral-400">{meters}</span>}
      <button
        type="button"
        onClick={onOpen}
        aria-label={`Open "${t.name}"`}
        className="hidden min-h-11 shrink-0 items-center rounded-lg border border-neutral-700 px-4 text-sm text-neutral-100 hover:border-neutral-500 lg:inline-flex"
      >
        Open
      </button>
      {onShare && (
        <button
          type="button"
          onClick={onShare}
          data-testid="trip-share-open"
          aria-label={`Share "${t.name}"`}
          className="inline-flex min-h-11 min-w-11 shrink-0 items-center justify-center gap-2 rounded-lg text-neutral-300 hover:text-neutral-100 lg:min-w-0 lg:border lg:border-neutral-700 lg:px-4 lg:text-sm lg:hover:border-neutral-500"
        >
          <ShareIcon />
          <span className="hidden lg:inline">Share</span>
        </button>
      )}
    </li>
  );
}

export function WhereToHome({
  fromLabel,
  departure,
  departureLabel,
  rangeText,
  saved,
  savedLoading,
  canShare,
  onPickPlace,
  onOpenSaved,
  onShareSaved,
  onShowSaved,
}: {
  // "From your truck" or the place the trip starts from.
  fromLabel: string;
  // The departure picker, shown under the chips when "Leave now" is tapped.
  departure: React.ReactNode;
  departureLabel: string;
  // The truck's range line, or null when its state is unknown or old.
  rangeText: string | null;
  saved: SavedTrip[];
  savedLoading: boolean;
  canShare: boolean;
  onPickPlace: (r: GeocodeResult) => void;
  onOpenSaved: (t: SavedTrip) => void;
  onShareSaved: (t: SavedTrip) => void;
  onShowSaved: () => void;
}) {
  const { distanceUnit } = usePreferences();
  const [timeOpen, setTimeOpen] = useState(false);
  const [query, setQuery] = useState("");
  const timeId = useId();
  const geo = useGeocode(query);
  const miles = (m: number) => `${Math.round(distanceUnit === "mi" ? m / 1609.344 : m / 1000)} ${distanceUnit}`;
  const meters = (t: SavedTrip) => {
    const m = tripMeters(t);
    return m == null ? null : miles(m);
  };
  const q = query.trim().toLowerCase();
  const typing = q.length > 0;
  const matches = typing ? saved.filter((t) => `${t.name} ${tripSubtitle(t.inputs)}`.toLowerCase().includes(q)) : [];
  const shown = saved.slice(0, SAVED_SHOWN);

  return (
    <div data-where-to className="max-w-[720px] space-y-4">
      <div className="flex items-center gap-1 rounded-2xl border border-neutral-700 bg-neutral-900 px-3">
        <svg aria-hidden width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="shrink-0 text-neutral-400">
          <circle cx="11" cy="11" r="7" />
          <path d="m20 20-3.5-3.5" />
        </svg>
        <input
          type="text"
          inputMode="search"
          enterKeyHint="search"
          autoComplete="off"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => e.key === "Escape" && setQuery("")}
          placeholder="Where to?"
          aria-label="Where to?"
          className="min-h-12 min-w-0 flex-1 bg-transparent px-1 text-[16px] text-neutral-100 placeholder-neutral-400 focus:outline-hidden"
        />
        {typing && (
          <button type="button" aria-label="Clear" onClick={() => setQuery("")} className="inline-flex min-h-11 min-w-11 shrink-0 items-center justify-center text-neutral-400 hover:text-neutral-200">
            <svg aria-hidden width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
              <path d="M6 6l12 12M18 6 6 18" />
            </svg>
          </button>
        )}
      </div>

      {typing ? (
        <div data-where-results className="space-y-4">
          {matches.length > 0 && (
            <section aria-label="Your plans">
              <h2 className="ui-label">Your plans</h2>
              <ul className="mt-1 divide-y divide-neutral-800">
                {matches.map((t) => (
                  <TripRow key={t.id} t={t} onOpen={() => onOpenSaved(t)} onShare={canShare && t.plan ? () => onShareSaved(t) : undefined} meters={meters(t)} />
                ))}
              </ul>
            </section>
          )}
          <section aria-label="Places">
            {geo.items.length > 0 && <h2 className="ui-label">Places</h2>}
            {geo.items.length > 0 && (
              <ul className="mt-1 divide-y divide-neutral-800">
                {geo.items.map((r) => (
                  <li key={`${r.latitude},${r.longitude}`}>
                    <button type="button" onClick={() => onPickPlace(r)} className="block min-h-11 w-full py-2.5 text-left">
                      <span className="block break-words text-base text-neutral-100">{r.name}</span>
                      {area(r) && <span className="block break-words text-xs text-neutral-400">{area(r)}</span>}
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {geo.active && geo.pending && geo.items.length === 0 && (
              <p role="status" className="py-2 text-sm text-neutral-400">Searching…</p>
            )}
            {geo.empty && <p role="status" className="py-2 text-sm text-neutral-400">No places match "{query.trim()}". Try a city, a park or an address.</p>}
            {geo.failed && <p role="alert" className="py-2 text-sm text-rose-400">Place search is unavailable right now. Try again in a moment.</p>}
            {!geo.active && <p className="py-2 text-sm text-neutral-400">Keep typing: two letters start the search.</p>}
          </section>
        </div>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-1.5">
            <span data-plan-chip className="inline-flex max-w-full items-center gap-1.5">
              <span aria-hidden className="h-2 w-2 shrink-0 rounded-full bg-emerald-400" />
              <span className="min-w-0 break-words">{fromLabel}</span>
            </span>
            <button type="button" data-plan-chip aria-expanded={timeOpen} aria-controls={timeId} onClick={() => setTimeOpen((v) => !v)}>
              {departureLabel}
            </button>
          </div>
          <div id={timeId} hidden={!timeOpen} className="rounded-xl border border-neutral-800 px-3 pb-3 pt-2">
            {departure}
          </div>
          {rangeText && (
            <p data-range-line className="break-words text-sm text-neutral-300">
              {rangeText}
            </p>
          )}
          <section aria-label="Saved trips" data-where-saved>
            <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
              <h2 className="ui-label">Recent and saved</h2>
              {saved.length > 0 && (
                <button type="button" onClick={onShowSaved} className="inline-flex min-h-11 items-center text-sm text-emerald-300 hover:text-emerald-200">
                  Saved trips ({saved.length}) ›
                </button>
              )}
            </div>
            {savedLoading ? (
              <p className="py-3 text-sm text-neutral-500">Loading…</p>
            ) : shown.length === 0 ? (
              <p className="py-2 text-sm text-neutral-400">Trips you plan and save show up here, one tap from planning again.</p>
            ) : (
              <ul className="mt-1 divide-y divide-neutral-800">
                {shown.map((t) => (
                  <TripRow key={t.id} t={t} onOpen={() => onOpenSaved(t)} onShare={canShare && t.plan ? () => onShareSaved(t) : undefined} meters={meters(t)} />
                ))}
              </ul>
            )}
          </section>
        </>
      )}
    </div>
  );
}
