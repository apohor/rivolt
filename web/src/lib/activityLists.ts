import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { backend, type VehicleState } from "./api";

// The drive and charge lists are big (thousands of rows) and change only when a drive or charge closes, so they are
// fetched once, kept for a few minutes, and refreshed when a session ends, not on a timer.
export const LIST_STALE_MS = 5 * 60_000;

// The drive list without route polylines (the lists draw no maps), which is most of its weight.
export const drivesAllQuery = () => ({ queryKey: ["drives", "all"] as const, queryFn: () => backend.allDrives({ slim: true }), staleTime: LIST_STALE_MS });
// The same list with each drive's route, fetched only where a map of many drives is drawn.
export const drivesRoutesQuery = () => ({ queryKey: ["drives", "all", "routes"] as const, queryFn: () => backend.allDrives(), staleTime: LIST_STALE_MS });

// Puts each drive's stored route (from the full list) on the rows a map is about to draw.
export function attachRoutes<T extends { ID: string; RoutePolyline?: string }>(rows: T[], full: { ID: string; RoutePolyline?: string }[] | undefined): T[] {
  if (!full) return rows;
  const byID = new Map(full.map((d) => [d.ID, d.RoutePolyline]));
  return rows.map((d) => ({ ...d, RoutePolyline: byID.get(d.ID) || d.RoutePolyline }));
}
export const chargesAllQuery = () => ({ queryKey: ["charges", "all"] as const, queryFn: () => backend.allCharges(), staleTime: LIST_STALE_MS });

// A drive or a charge is under way: the truck is in gear, moving, or the charger is pulling power.
export function sessionInFlight(s: Pick<VehicleState, "speed_kph" | "gear" | "charger_power_kw" | "charger_state">): boolean {
  return (
    (s.speed_kph ?? 0) > 1 ||
    s.gear === "Drive" ||
    s.gear === "Reverse" ||
    s.gear === "D" ||
    s.gear === "R" ||
    (s.charger_power_kw ?? 0) > 0.1 ||
    s.charger_state === "charging_active" ||
    s.charger_state === "charging_dc"
  );
}

// Refreshes the lists when a vehicle goes from a session under way to none. It listens to the vehicle-state polls the
// pages already make, so it adds no requests of its own.
export function useRefreshListsWhenSessionCloses() {
  const qc = useQueryClient();
  useEffect(() => {
    const was = new Map<string, boolean>();
    return qc.getQueryCache().subscribe((event) => {
      if (event.type !== "updated" || event.action.type !== "success") return;
      const key = event.query.queryKey;
      if (key[0] !== "rivian" || key[1] !== "state") return;
      const state = event.query.state.data as VehicleState | undefined;
      if (!state) return;
      const id = String(key[2] ?? "");
      const now = sessionInFlight(state);
      if (was.get(id) === true && !now) {
        void qc.invalidateQueries({ queryKey: ["drives"] });
        void qc.invalidateQueries({ queryKey: ["charges"] });
      }
      was.set(id, now);
    });
  }, [qc]);
}
