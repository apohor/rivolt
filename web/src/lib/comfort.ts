import type { VehicleState } from "./api";
import { formatTemperature, type TemperatureUnit } from "./preferences";

// The Now card's one comfort line, from what the truck reports: cabin temperature, preconditioning, pet mode, climate
// hold. The truck's own words, never worked out: undefined / unknown / off / disabled / faulty are left out, and a cabin
// reading of 0 means "no reading". Plain text on purpose: pet mode is never coloured or marked as an alert, and the line
// says nothing about the animal's safety or a temperature limit.
type Comfort = Pick<VehicleState, "cabin_temp_c" | "cabin_preconditioning_status" | "pet_mode_status" | "cabin_hold_status">;

const clean = (s: string | undefined | null) => (s ?? "").toLowerCase().trim();
// The same words the live panel reads as on.
const isOn = (s: string | undefined | null) => ["true", "on", "active", "1"].includes(clean(s));

// The cabin temperature in whole degrees, for its own row beside Outside; null when the truck gave none.
export function cabinValue(s: Pick<VehicleState, "cabin_temp_c">, unit: TemperatureUnit): string | null {
  const c = s.cabin_temp_c;
  return typeof c === "number" && Number.isFinite(c) && c !== 0 && c > -50 && c < 80 ? formatTemperature(c, unit) : null;
}

// The rest of the comfort line: preconditioning, pet mode, climate hold (the cabin temperature has its own row).
export function comfortParts(s: Comfort): string[] {
  const out: string[] = [];
  const precon = clean(s.cabin_preconditioning_status);
  if (precon === "preconditioning") out.push("Preconditioning");
  else if (precon === "preconditioned") out.push("Preconditioned");
  if (isOn(s.pet_mode_status)) out.push("Pet mode on");
  if (isOn(s.cabin_hold_status)) out.push("Climate hold");
  return out;
}
