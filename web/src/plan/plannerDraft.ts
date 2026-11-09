import type { DriveMode, Selection } from "./plannerHelpers";
import type { OvernightOptions } from "./days";
import type { RoutePrefs } from "./routePrefs";

// What the planner form keeps across a reload that the app did not ask for: asset-guard reloads a tab whose JS chunk went missing in a deploy,
// and a reload empties React state. The page saves its form here when asset-guard says "rivolt:before-reload", and takes it back (once) on its
// next load. sessionStorage, so it goes with the tab and never outlives it; a draft older than DRAFT_MAX_AGE_MS is a different session of work
// and is dropped. Storage may throw (private windows); then nothing is saved and nothing breaks. The plan on screen is not kept, only the
// inputs: planning again is one tap.
export const DRAFT_KEY = "rivolt.plannerDraft";
export const DRAFT_EVENT = "rivolt:before-reload";
export const DRAFT_MAX_AGE_MS = 10 * 60 * 1000;
const DRAFT_VERSION = 1;

export type PlannerDraft = {
  origin: Selection;
  destination: Selection;
  extraStops: NonNullable<Selection>[];
  overnightFlags: boolean[];
  overnightOpts: Record<string, OvernightOptions>;
  maxOvernightSoCPct: number;
  overnightDepartureLocal: string;
  overnightL2Kw: number;
  targetSoc: string;
  startingSoc: string;
  driveMode: DriveMode;
  hasAdapter: boolean;
  departureAt: string;
  routePrefs: RoutePrefs;
};

// hasContent: a draft worth saving or restoring has at least one end or a stop; an empty form has nothing to lose.
function hasContent(d: PlannerDraft): boolean {
  return !!(d.origin || d.destination || d.extraStops.length > 0);
}

export function savePlannerDraft(d: PlannerDraft, now: number = Date.now()): void {
  if (!hasContent(d)) return;
  try {
    window.sessionStorage.setItem(DRAFT_KEY, JSON.stringify({ v: DRAFT_VERSION, at: now, draft: d }));
  } catch {
    /* ignore */
  }
}

// takePlannerDraft returns the saved draft once and removes it, or null when there is none, it is too old, or it is not ours.
export function takePlannerDraft(now: number = Date.now()): PlannerDraft | null {
  try {
    const raw = window.sessionStorage.getItem(DRAFT_KEY);
    if (!raw) return null;
    window.sessionStorage.removeItem(DRAFT_KEY);
    const parsed = JSON.parse(raw) as { v?: number; at?: number; draft?: PlannerDraft };
    if (parsed.v !== DRAFT_VERSION || typeof parsed.at !== "number" || !parsed.draft) return null;
    if (now - parsed.at > DRAFT_MAX_AGE_MS || now < parsed.at) return null;
    const d = parsed.draft;
    if (!Array.isArray(d.extraStops) || !Array.isArray(d.overnightFlags) || typeof d.targetSoc !== "string") return null;
    return hasContent(d) ? d : null;
  } catch {
    return null;
  }
}
