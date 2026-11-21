import { haversineM, type ChargeFlavor, type Place } from "./nowState";

export const CHARGER_LABEL: Record<ChargeFlavor, string> = { home_l2: "Home charger", dcfc: "Fast charger", away_ac: "Public charger" };

// The label a Rivian-flagged session carries on its own (the truck says so), else the rule's.
export function chargerLabel(flavor: ChargeFlavor | null, rivian = false): string | null {
  return rivian ? "Rivian charger" : flavor ? CHARGER_LABEL[flavor] : null;
}

// At home or away from the vehicle's fix and the home point; null when either is missing.
export function placeOf(home: { latitude: number; longitude: number } | null, fix: { latitude: number; longitude: number }): Place {
  const has = Number.isFinite(fix.latitude) && Number.isFinite(fix.longitude) && (fix.latitude !== 0 || fix.longitude !== 0);
  if (!home || !has) return "unknown";
  return haversineM(home, fix) <= 250 ? "home" : "away";
}
