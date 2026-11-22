// The charge pill on the plan map: "25 min · 40→80%". plan_v2 puts the stop's number in front ("2 · 25 min · 40→80%"): the itinerary row carries the same one.
export function chargePillText(minutes: number, arrivalSoC: number, departureSoC: number, number?: number | null): string {
  return `${number != null ? `${number} · ` : ""}${minutes} min · ${Math.round(arrivalSoC)}→${Math.round(departureSoC)}%`;
}
