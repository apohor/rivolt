// The time left in a charge: the truck's own figure when it is trustworthy, else ours. The same rule as
// internal/rivian/charge_eta.go and ios/Rivolt/Views/ChargeETA.swift; all three pass
// internal/rivian/testdata/charge_eta_vectors.json. Change a vector before changing any of them.

/** A longer figure is stale or nonsense (a day). */
export const ETA_MAX_MINS = 24 * 60;
/** The truck's figure is dropped when under a quarter or over four times ours (a taper makes it longer than a line). */
export const ETA_GUARD_LOW = 0.25;
export const ETA_GUARD_HIGH = 4;
const MIN_KW = 0.1;

export type EtaSource = "truck" | "estimate";

/** Our straight-line estimate in whole minutes (at least 1), or 0 when anything is unknown or at the limit. */
export function ownChargeEtaMins(socPct: number, limitPct: number, packKwh: number, powerKw: number): number {
  if (limitPct <= socPct || packKwh <= 0 || powerKw <= MIN_KW) return 0;
  const kwh = ((limitPct - socPct) / 100) * packKwh;
  return Math.max(1, Math.ceil((kwh / powerKw) * 60));
}

/**
 * What to show. truckMins is the truck's figure (0 when none), validity its charging_time_estimation_validity,
 * limitPct the charge limit (0 when unknown), oursMins our estimate (0 when none).
 */
export function pickChargeEta(
  truckMins: number,
  validity: string | null | undefined,
  socPct: number,
  limitPct: number,
  oursMins: number,
): { minutes: number; source: EtaSource | null } {
  let ok = truckMins > 0 && truckMins <= ETA_MAX_MINS && Number.isFinite(truckMins) && (validity ?? "").trim().toLowerCase() !== "invalid";
  if (ok && limitPct > 0 && socPct >= limitPct) ok = false;
  if (ok && oursMins > 0 && (truckMins < ETA_GUARD_LOW * oursMins || truckMins > ETA_GUARD_HIGH * oursMins)) ok = false;
  if (ok) return { minutes: truckMins, source: "truck" };
  if (oursMins > 0) return { minutes: oursMins, source: "estimate" };
  return { minutes: 0, source: null };
}
