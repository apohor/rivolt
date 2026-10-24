// A charge whose energy was lost (a bug before the fix) and worked out afterwards from the battery's change in charge level. Its kWh, and
// the cost that follows from it, are estimates: shown with the same "~" and dotted underline the estimated costs use, never as metered.
// The API says so per charge: `EnergyEstimated` on /api/charges, `energy_estimated` on the activity feed.
export const ENERGY_ESTIMATED_NOTE = "Estimated from the battery's change. A Rivolt bug lost the energy count for this charge.";

type Flag = { EnergyEstimated?: boolean; energy_estimated?: boolean };

export const isEnergyEstimated = (c: Flag): boolean => c.EnergyEstimated === true || c.energy_estimated === true;

// "12.4 kWh", or "~12.4 kWh" when estimated; "—" without an energy figure.
export function energyKWhText(c: Flag & { EnergyAddedKWh: number }, digits = 1): string {
  if (!(c.EnergyAddedKWh > 0)) return "—";
  return `${isEnergyEstimated(c) ? "~" : ""}${c.EnergyAddedKWh.toFixed(digits)} kWh`;
}

// Tailwind classes for an estimated figure, as on the estimated costs.
export const ESTIMATED_FIGURE = "underline decoration-dotted underline-offset-4";

// Under a month's energy total that includes an estimated charge.
export const ENERGY_ESTIMATED_TOTAL_NOTE =
  "Some charges this month have an estimated energy figure (marked ~): worked out from the battery's change because a Rivolt bug lost their energy count.";
