import { useCallback, useEffect, useId, useState } from "react";

// The planner's route bar: from, to, departure and starting SoC. The state
// lives here, in one hook, so the classic form and the map-first bar share
// exactly the same values and the same plan call; only the markup differs.

export type RouteEnd = { lat: number; lon: number; label: string } | null;

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

// useRouteBarCollapse drives the phone route bar. With a result on screen it
// folds to its one-line summary (resultKey identifies the result; a NEW result
// folds it again even if it was open for editing, so replanning from the open
// bar returns to the result); without one it is open. The Edit/Done button
// toggles it, and bodyId is the id the button controls.
export function useRouteBarCollapse(hasResult: boolean, resultKey: unknown) {
  const [editing, setEditing] = useState(false);
  const bodyId = useId();
  useEffect(() => {
    setEditing(false);
  }, [resultKey, hasResult]);
  const collapsed = hasResult && !editing;
  const toggle = useCallback(() => setEditing((v) => !v), []);
  return { collapsed, toggle, bodyId };
}
