import { useCallback, useEffect, useMemo, useState } from "react";
import type { Vehicle } from "./api";

// Which vehicle the user is currently "looking at" on the
// single-vehicle pages (HomePage battery stat, live tile, etc).
//
// Before: every page picked `vehicles[0]`, which:
//   - silently hid the second car in a two-car household
//     (the canonical Rivian case: R1T + R1S, or R1S + R2)
//   - made the battery KPI jump between cars whenever the
//     backend reordered the /api/vehicles response
//
// After: the selection lives in localStorage so it's stable
// across reloads, stable across /api/vehicles reordering, and
// explicit — a picker in the hero lets the user flip between
// cars. The hook degrades cleanly:
//   - 0 vehicles → selectedID is undefined (pages render
//     empty-state as they did before)
//   - 1 vehicle  → selectedID is always that one, picker
//     hides itself (no UI clutter for the common case)
//   - 2+        → picker renders, localStorage remembers
//
// Keyed per-user is out of scope here: the whole UI is
// scoped to one auth'd user at a time, so a bare key is fine.

const STORAGE_KEY = "rivolt.selectedVehicleID";

// readStoredID is a safe wrapper — localStorage throws in
// private-browsing / SSR-ish contexts, and we never want
// selection logic to crash the page.
function readStoredID(): string | null {
  try {
    return window.localStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
}

function writeStoredID(id: string | null) {
  try {
    if (id == null) {
      window.localStorage.removeItem(STORAGE_KEY);
    } else {
      window.localStorage.setItem(STORAGE_KEY, id);
    }
  } catch {
    // non-fatal; next reload just falls back to vehicles[0].
  }
}

export interface SelectedVehicle {
  // id of the vehicle the UI should act on, or undefined when
  // the user has no vehicles registered.
  selectedID: string | undefined;
  // Stable reference into the passed-in vehicles array for
  // callers that need the whole object (image, model, name).
  selected: Vehicle | undefined;
  // Call to change the selection. Persists to localStorage.
  // Accepts an id present in the current list; otherwise a
  // no-op so a stale picker choice can't poison state.
  select: (id: string) => void;
}

/**
 * useSelectedVehicle binds a persistent "which vehicle is the
 * active one" choice to the list returned by /api/vehicles.
 *
 * Selection resolution order:
 *   1. The id stored in localStorage, if it still appears in
 *      the list (the car wasn't removed between sessions).
 *   2. vehicles[0].id (backwards compatible with the old
 *      hard-coded behaviour).
 *   3. undefined when the list is empty.
 *
 * The storage value is self-healing: any resolution that
 * falls off step 1 is written back so the user's next refresh
 * matches their current view.
 */
export function useSelectedVehicle(
  vehicles: Vehicle[] | undefined,
): SelectedVehicle {
  // Lazily seed from localStorage exactly once. Re-reads on
  // every render would fight the setter within a single tick.
  const [stored, setStored] = useState<string | null>(() =>
    typeof window !== "undefined" ? readStoredID() : null,
  );

  const list = vehicles ?? [];
  const selectedID = useMemo(() => {
    if (list.length === 0) return undefined;
    if (stored && list.some((v) => v.id === stored)) return stored;
    return list[0].id;
  }, [list, stored]);

  // Self-heal: if our resolved id differs from what's on
  // disk (stale id, or first-ever boot), write it back so a
  // refresh lands on the same car.
  useEffect(() => {
    if (selectedID && selectedID !== stored) {
      writeStoredID(selectedID);
      setStored(selectedID);
    }
  }, [selectedID, stored]);

  const selected = useMemo(
    () => list.find((v) => v.id === selectedID),
    [list, selectedID],
  );

  const select = useCallback(
    (id: string) => {
      // Guard against stale picker choices — a component that
      // captured a removed vehicle's id shouldn't be able to
      // poison state.
      if (!list.some((v) => v.id === id)) return;
      writeStoredID(id);
      setStored(id);
    },
    [list],
  );

  return { selectedID, selected, select };
}
