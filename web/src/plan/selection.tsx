import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

// The selected stop of the planner result, shared by everything that shows
// stops: the itinerary list, the map markers, and any summary that follows
// the selection. The value is an index into route.Waypoints (the same meaning
// as TripRouteMap's selectedIdx), or null for "nothing selected".
export interface SelectedStop {
  selected: number | null;
  select: (idx: number | null) => void;
  // toggle selects idx, or clears it when it is already the selection (a
  // second tap on a stop, or on its marker, deselects it).
  toggle: (idx: number) => void;
  clear: () => void;
}

const SelectionContext = createContext<SelectedStop | null>(null);

// useOwnSelection is selection state that resets whenever resetKey changes
// (a new plan or route must not inherit the old selection).
function useOwnSelection(resetKey: unknown): SelectedStop {
  const [state, setState] = useState<{ key: unknown; idx: number | null }>({ key: resetKey, idx: null });
  // A stale key means the route changed since the selection was made: read
  // it as cleared without waiting for an effect (no flash of a wrong stop).
  const selected = Object.is(state.key, resetKey) ? state.idx : null;
  useEffect(() => {
    setState((s) => (Object.is(s.key, resetKey) ? s : { key: resetKey, idx: null }));
  }, [resetKey]);
  const select = useCallback((idx: number | null) => setState({ key: resetKey, idx }), [resetKey]);
  const toggle = useCallback(
    (idx: number) => setState((s) => ({ key: resetKey, idx: Object.is(s.key, resetKey) && s.idx === idx ? null : idx })),
    [resetKey],
  );
  const clear = useCallback(() => setState({ key: resetKey, idx: null }), [resetKey]);
  return useMemo(() => ({ selected, select, toggle, clear }), [selected, select, toggle, clear]);
}

// PlanSelectionProvider holds the selection for one plan result. routeKey
// identifies the route being shown (any value; compared with Object.is).
export function PlanSelectionProvider({ routeKey, children }: { routeKey: unknown; children: ReactNode }) {
  const value = useOwnSelection(routeKey);
  return <SelectionContext.Provider value={value}>{children}</SelectionContext.Provider>;
}

// useSelectedStop returns the selection for routeKey. With shared (the
// primary route in the map-first layout) and a provider above, that is the
// provider's, so every component using it stays in step; otherwise it is the
// component's own, which is how the classic route cards keep their
// independent per-card selection.
export function useSelectedStop(routeKey: unknown, opts: { shared?: boolean } = {}): SelectedStop {
  const own = useOwnSelection(routeKey);
  const ctx = useContext(SelectionContext);
  return opts.shared && ctx ? ctx : own;
}
