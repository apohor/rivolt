// The charge pill on the plan map: "25 min · 40→80%".
export function chargePillText(minutes: number, arrivalSoC: number, departureSoC: number): string {
  return `${minutes} min · ${Math.round(arrivalSoC)}→${Math.round(departureSoC)}%`;
}
