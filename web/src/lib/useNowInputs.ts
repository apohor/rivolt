import { useQuery } from "@tanstack/react-query";
import { backend } from "./api";

// The two reads the Now card and the Live page both need for the charger label; one query key each, so React Query serves the second
// screen from the first's cache.
export const useHomeLocation = () =>
  useQuery({ queryKey: ["settings", "home-location"], queryFn: () => backend.homeLocationGet(), staleTime: 5 * 60_000, retry: 1 });

// Null when absent (an older server).
export const useVehicleNow = (vehicleID: string) =>
  useQuery({ queryKey: ["vehicle", "now", vehicleID], queryFn: () => backend.vehicleNow(vehicleID), refetchInterval: 15_000, retry: 0 });

// The home rate and currency the Now card prices an AC charge with; the Live page shares the key.
export const useChargingSettings = () =>
  useQuery({ queryKey: ["settings", "charging"], queryFn: () => backend.getChargingSettings(), staleTime: 5 * 60_000, retry: 1 });
