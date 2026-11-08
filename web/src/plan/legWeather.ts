import type { DistanceUnit, TemperatureUnit } from "../lib/preferences";
import type { TripWeatherAdjLeg } from "../lib/api";

// What a leg's weather says, as facts. The map badge and the stop row show these; the energy effect lives only in the stop's detail line,
// worded as an estimate. Rain shows only when the server applied its wet factor (precip over 0.5 mm); snow only when the server says so.
const WET_MM = 0.5;
// A headwind or tailwind under this (km/h) is noise, not something to tell a driver about.
const WIND_MIN_KPH = 8;
const KPH_PER_MPH = 1.609344;

export type LegWeatherFacts = {
  // "41 °F" (tag) and "41°" (badge); absent when the leg has no temperature.
  temp?: string;
  tempShort?: string;
  // "14 °F" when the air temperature and the feels-like differ; the leg's details say it, the badge and tags do not.
  feels?: string;
  // Along the direction of travel: "head" blows against it, "tail" with it. speed is in the account's unit.
  wind?: { kind: "head" | "tail"; speed: number; unit: "mph" | "km/h" };
  rain: boolean;
  snow: boolean;
};

const roundTemp = (c: number, unit: TemperatureUnit) => Math.round(unit === "f" ? c * 1.8 + 32 : c);
// A real minus, not a hyphen.
const signed = (n: number) => (n < 0 ? "−" : "") + Math.abs(n);

export function legWeatherFacts(leg: TripWeatherAdjLeg | undefined, tempUnit: TemperatureUnit, distUnit: DistanceUnit): LegWeatherFacts | null {
  if (!leg) return null;
  const out: LegWeatherFacts = { rain: false, snow: leg.snow === true };
  // The air temperature is what the truck shows; until the server sends it, temp_c (feels like, when there is one) stands in.
  const air = leg.air_temp_c ?? leg.temp_c;
  if (air != null && Number.isFinite(air)) {
    const t = roundTemp(air, tempUnit);
    out.temp = `${signed(t)} °${tempUnit === "f" ? "F" : "C"}`;
    out.tempShort = `${signed(t)}°`;
    if (leg.air_temp_c != null && leg.temp_c != null && Number.isFinite(leg.temp_c)) {
      const f = roundTemp(leg.temp_c, tempUnit);
      if (f !== t) out.feels = `${signed(f)} °${tempUnit === "f" ? "F" : "C"}`;
    }
  }
  const hw = leg.headwind_kph;
  if (hw != null && Number.isFinite(hw) && Math.abs(hw) >= WIND_MIN_KPH) {
    out.wind = distUnit === "mi"
      ? { kind: hw > 0 ? "head" : "tail", speed: Math.round(Math.abs(hw) / KPH_PER_MPH), unit: "mph" }
      : { kind: hw > 0 ? "head" : "tail", speed: Math.round(Math.abs(hw)), unit: "km/h" };
  }
  out.rain = !out.snow && leg.precip_mm != null && leg.precip_mm > WET_MM;
  if (!out.temp && !out.wind && !out.rain && !out.snow) return null;
  return out;
}

// The stop row's tags: "41 °F", "25 mph headwind", "rain" / "snow expected".
export function legWeatherTags(f: LegWeatherFacts | null): string[] {
  if (!f) return [];
  const tags: string[] = [];
  if (f.temp) tags.push(f.temp);
  if (f.wind) tags.push(`${f.wind.speed} ${f.wind.unit} ${f.wind.kind}wind`);
  if (f.snow) tags.push("snow expected");
  else if (f.rain) tags.push("rain");
  return tags;
}

// The one estimate line (stop detail and leg popup). The multiplier is the server's per-leg figure: weather, times the driver profile when
// the plan used one. Under 1 % either way it says nothing.
export function legEstimateLine(multiplier: number, withSetup: boolean): string | null {
  const pct = Math.round((multiplier - 1) * 100);
  if (!Number.isFinite(pct) || pct === 0) return null;
  return `${withSetup ? "Weather and your setup" : "Weather"}: ${pct < 0 ? "−" : "+"}${Math.abs(pct)}% (estimate)`;
}

// The climb note shown once the server sends it: "Climb +1,900 ft · descent −2,200 ft", in the account's unit.
export function climbLine(climbM: number | undefined, descentM: number | undefined, distUnit: DistanceUnit): string | null {
  if (climbM == null || descentM == null) return null;
  const f = (m: number) => Math.round(distUnit === "mi" ? m * 3.28084 : m).toLocaleString("en-US");
  const u = distUnit === "mi" ? "ft" : "m";
  return `Climb +${f(climbM)} ${u} · descent −${f(descentM)} ${u}`;
}
