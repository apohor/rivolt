import type { TripRoute } from "../lib/api";

// A charging stop is any planned waypoint that is not the start, the end or a plain via. The planner's
// type names differ by source and case ("charger", "DC_CHARGE_STATION"), so this is what it is not.
export const isChargingWaypoint = (w: { WaypointType: string }): boolean => {
  const t = w.WaypointType.toLowerCase();
  return t !== "origin" && t !== "destination" && t !== "waypoint" && t !== "other";
};

// The number of charging stops as the plan_run event's bucket.
export const stopsBucket = (n: number): string => (n <= 0 ? "0" : n <= 2 ? "1_2" : "3_plus");

export const chargingStopCount = (route: Pick<TripRoute, "Waypoints">): number => route.Waypoints.filter(isChargingWaypoint).length;
