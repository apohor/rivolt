import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { usePreferences } from "../lib/preferences";

// The user's own range estimate near this temperature.
export function useColdRange(vehicleID: string, tempC: number | null | undefined) {
  const { distanceUnit } = usePreferences();
  const units = distanceUnit === "km" ? "metric" : "imperial";
  const rounded = tempC == null ? 0 : Math.round(tempC);
  const q = useQuery({
    queryKey: ["cold-range", "now", vehicleID, units, rounded],
    queryFn: () => backend.coldRange(vehicleID, rounded, [], units),
    enabled: tempC != null,
    staleTime: 30 * 60_000,
    retry: 0,
  });
  return q.data ?? null;
}
