import type { HookupId } from "./hookups";


export type StopAdder = (stop: { lat: number; lon: number; label: string }) => void;
// An overnight can arrive with the charger power the listing gave (kW).
// `thenVia` is a place the next day starts at (the fast charger after a hotel with no charger), added as a plain stop after the night.
export type OvernightAdder = (stop: { lat: number; lon: number; label: string; l2Kw?: number; hookup?: HookupId; campground?: boolean; thenVia?: { lat: number; lon: number; label: string } }) => void;

// PreviewResult is the deltas the map's "Preview impact" popup uses
// to show what adding a candidate stop would do BEFORE the user
// commits. Negative values mean "this candidate would improve",
// positive means "would worsen", except for delta_arrival_soc_pct
// where positive = arrives higher (better).
//
// Stop-by-stop comparison fields make Rivian's reoptimization
// visible: if the planner drops an existing stop because the
// candidate provides the same energy on the corridor, the popup
// can call that out instead of just showing "+55 min" with no
// explanation of where the time went.
export type PreviewResult = {
  delta_total_time_sec: number;
  delta_arrival_soc_pct: number;
  delta_stop_count: number;
  delta_cost: number;
  currency: string;
  stops_kept: number;
  stops_dropped: number;
  stops_added: number;
  // Name of an existing planned stop the candidate effectively
  // replaced (existing dropped AND candidate is in the new plan's
  // charging list within proximity). Empty when no substitution
  // happened.
  substituted_for?: string;
};

export type StopPreviewer = (
  stop: { lat: number; lon: number; label: string },
) => Promise<PreviewResult | null>;

// ViaStopList renders existing via stops with remove buttons and a
// search field for adding new ones by geocode.
