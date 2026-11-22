import { useCallback, useEffect, useId, useRef, useState } from "react";

// The planner's route bar: from, to, departure and starting SoC. The state
// lives here, in one hook, so the classic form and the map-first bar share
// exactly the same values and the same plan call; only the markup differs.

// `place` is the short place name, set where the place is picked (a saved place's own label, a geocoder hit's locality or name); older drafts lack it.
export type RouteEnd = { lat: number; lon: number; label: string; place?: string } | null;

export interface RouteBarState {
  origin: RouteEnd;
  destination: RouteEnd;
  // datetime-local string (local time, no zone); "" departs now.
  departureAt: string;
  // Percent as typed; "" means "use the live vehicle state".
  startingSoc: string;
  setOrigin: (v: RouteEnd) => void;
  setDestination: (v: RouteEnd) => void;
  setDepartureAt: (v: string) => void;
  setStartingSoc: (v: string) => void;
  // swap exchanges from and to (the reverse trip).
  swap: () => void;
  // canPlan: both ends are chosen.
  canPlan: boolean;
}

export function useRouteBarState(): RouteBarState {
  // Both ends in one state object, so swap is a single atomic update.
  const [ends, setEnds] = useState<{ origin: RouteEnd; destination: RouteEnd }>({ origin: null, destination: null });
  const [departureAt, setDepartureAt] = useState("");
  const [startingSoc, setStartingSoc] = useState("");
  const setOrigin = useCallback((origin: RouteEnd) => setEnds((e) => ({ ...e, origin })), []);
  const setDestination = useCallback((destination: RouteEnd) => setEnds((e) => ({ ...e, destination })), []);
  const swap = useCallback(() => setEnds((e) => ({ origin: e.destination, destination: e.origin })), []);
  return {
    origin: ends.origin,
    destination: ends.destination,
    departureAt,
    startingSoc,
    setOrigin,
    setDestination,
    setDepartureAt,
    setStartingSoc,
    swap,
    canPlan: !!ends.origin && !!ends.destination,
  };
}

// formatRouteSummary is the one-line "From → To · 80% · Sat 8:00 AM" the
// collapsed bar shows. Missing ends show "…"; SoC and departure are left out
// when empty.
export function formatRouteSummary(
  s: Pick<RouteBarState, "origin" | "destination" | "startingSoc" | "departureAt">,
  locale?: string,
): string {
  const parts = [`${s.origin?.label ?? "…"} → ${s.destination?.label ?? "…"}`];
  if (s.startingSoc.trim()) parts.push(`${s.startingSoc.trim()}%`);
  if (s.departureAt) {
    const d = new Date(s.departureAt);
    if (!Number.isNaN(d.getTime())) {
      parts.push(d.toLocaleString(locale, { weekday: "short", hour: "numeric", minute: "2-digit" }));
    }
  }
  return parts.join(" · ");
}

// formatResultSummaryParts is the plan_v2 summary line over a shown plan: "From → To · departure · starting charge". Unlike formatRouteSummary it
// always names the departure ("Leaves now" when none is set) and falls back to the live charge. The places are the city only (no state or country; the
// full labels are in `full`, for the title), so the departure and the charge, the `facts`, are never what gets cut.
// A street part begins with a house number ("12 Main St") or names a corner ("Main St & 5th Ave"). "St. George", "Mt. Shasta" and "Fort Collins" are places.
export const isStreetPart = (x: string) => /^\d/.test(x) || / & /.test(x);
// The short name of a geocoder hit: its locality when it has one (a street hit), else its name unless that is a street part.
export const geocodePlace = (r: { name: string; locality?: string }): string | undefined => r.locality || (isStreetPart(r.name) ? undefined : r.name);
// The place name is the one set at the pick (`place`). Without it (a draft from before) the label's first part, skipping a leading house number
// ("12 Main St, Austin, TX" is Austin).
const city = (end: RouteEnd | undefined) => {
  if (end?.place?.trim()) return end.place.trim();
  const parts = (end?.label ?? "").split(",").map((x) => x.trim()).filter(Boolean);
  const named = parts.findIndex((x) => !isStreetPart(x));
  return (named >= 0 ? parts[named] : parts[0]) || "…";
};
export function formatResultSummaryParts(
  s: Pick<RouteBarState, "origin" | "destination" | "startingSoc" | "departureAt">,
  liveSoc: number | null,
  locale?: string,
): { route: string; facts: string; full: string } {
  const facts: string[] = [];
  const d = s.departureAt ? new Date(s.departureAt) : null;
  facts.push(d && !Number.isNaN(d.getTime()) ? d.toLocaleString(locale, { weekday: "short", hour: "numeric", minute: "2-digit" }) : "Leaves now");
  if (s.startingSoc.trim()) facts.push(`${s.startingSoc.trim()}% charge`);
  else if (liveSoc != null) facts.push(`${Math.round(liveSoc)}% charge now`);
  const route = `${city(s.origin)} → ${city(s.destination)}`;
  return { route, facts: facts.join(" · "), full: `${s.origin?.label ?? "…"} → ${s.destination?.label ?? "…"} · ${facts.join(" · ")}` };
}

// useRouteBarCollapse drives the phone route bar. With a result on screen it
// folds to its one-line summary (resultKey identifies the result; a NEW result
// folds it again even if it was open for editing, so replanning from the open
// bar returns to the result); without one it is open. The Edit/Done button
// toggles it, and bodyId is the id the button controls.
// EDIT_EVENT: fired on window when Edit opens the form over a result (detail.open true) and when it closes again (false), so the phone's details
// sheet (a sibling, not a child) lowers to its handle while the form is open and comes back to where it was.
export const EDIT_EVENT = "rivolt:plan-edit";
export type EditEventDetail = { open: boolean };
function announceEdit(open: boolean) {
  if (typeof window !== "undefined") window.dispatchEvent(new CustomEvent<EditEventDetail>(EDIT_EVENT, { detail: { open } }));
}

export function useRouteBarCollapse(hasResult: boolean, resultKey: unknown) {
  const [editing, setEditing] = useState(false);
  const bodyId = useId();
  const wasEditing = useRef(false);
  wasEditing.current = editing;
  useEffect(() => {
    // A new result (or none) closes the form without Done: the sheet comes back all the same.
    if (wasEditing.current) announceEdit(false);
    setEditing(false);
  }, [resultKey, hasResult]);
  const collapsed = hasResult && !editing;
  const toggle = useCallback(() => {
    if (hasResult) announceEdit(!editing);
    setEditing((v) => !v);
  }, [hasResult, editing]);
  return { collapsed, toggle, bodyId };
}

// countNonDefaultOptions is the badge on "More options": how many of the
// hidden settings differ from their defaults (via stops, a target arrival
// SoC other than 20%, a non-default drive mode, the Tesla adapter).
export function countNonDefaultOptions(o: {
  viaStops: number;
  targetSoc: string;
  driveModeIsDefault: boolean;
  hasAdapter: boolean;
  avoidCount?: number;
}): number {
  const target = o.targetSoc.trim();
  return o.viaStops + (target !== "" && target !== "20" ? 1 : 0) + (o.driveModeIsDefault ? 0 : 1) + (o.hasAdapter ? 1 : 0) + (o.avoidCount ?? 0);
}
