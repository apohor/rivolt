import { MI_PER_KM, type DistanceUnit } from "./preferences";
import { EXAMPLE } from "./chargingCost";

/// "about 12–16 mi of range per hour" for a charger of `kw`: the sample truck's cold and typical efficiency (the same two figures the
/// charging-cost page uses) turn kilowatts into miles an hour, so the line is an estimate and says "about".
export function rangePerHour(kw: number, unit: DistanceUnit): string {
  const k = unit === "mi" ? 1 : 1 / MI_PER_KM;
  const lo = Math.round(kw * EXAMPLE.coldMilesPerKwh * k);
  const hi = Math.round(kw * EXAMPLE.milesPerKwh * k);
  return `about ${lo === hi ? lo : `${lo}–${hi}`} ${unit} of range per hour`;
}
