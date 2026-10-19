import type { Charge, Drive } from "./api";

// What the first-charge card says, from the history the Now page already has: the first charge (priced or
// not), or, before any charge, the first drive. Null once the first week is over, or with nothing recorded.
export const FIRST_CARD_DAYS = 7;

export type FirstChargeView =
  | { state: "A"; chargeID: string; kwh: number; cost: number; currency: string; rate: number; home: boolean; perMile?: { cost: number; miles: number } }
  | { state: "B"; chargeID: string; kwh: number }
  | { state: "C"; driveID: string; miles: number; miPerKwh: number | null };

const t = (iso: string): number => new Date(iso).getTime();
// Level 2 and slower is a home-style charge; anything faster is a DC fast charge, which has its own pricing.
const HOME_MAX_KW = 20;

export function firstChargeView(o: { charges: Charge[]; drives: Drive[]; homeRate: number; now?: Date }): FirstChargeView | null {
  const now = (o.now ?? new Date()).getTime();
  const charge = [...o.charges].filter((c) => c.EnergyAddedKWh > 0).sort((a, b) => t(a.StartedAt) - t(b.StartedAt))[0];
  const drive = [...o.drives].filter((d) => d.DistanceMi > 0).sort((a, b) => t(a.StartedAt) - t(b.StartedAt))[0];
  const anchor = Math.min(charge ? t(charge.StartedAt) : Infinity, drive ? t(drive.StartedAt) : Infinity);
  if (!Number.isFinite(anchor) || now - anchor > FIRST_CARD_DAYS * 86_400_000) return null;
  if (charge) {
    const cost = charge.Cost > 0 ? charge.Cost : (charge.estimated_cost ?? 0);
    if (o.homeRate > 0 && cost > 0) {
      // Only the first week, the same window the card is shown for: a long history is not "your first miles".
      const end = anchor + FIRST_CARD_DAYS * 86_400_000;
      const miles = o.drives.reduce((n, d) => n + (d.DistanceMi > 0 && t(d.StartedAt) >= anchor && t(d.StartedAt) <= end ? d.DistanceMi : 0), 0);
      return {
        state: "A",
        chargeID: charge.ID,
        kwh: charge.EnergyAddedKWh,
        cost,
        currency: charge.Currency || charge.estimated_currency || "USD",
        rate: o.homeRate,
        home: charge.MaxPowerKW <= HOME_MAX_KW,
        ...(miles >= 1 && cost / miles >= 0.005 ? { perMile: { cost: cost / miles, miles } } : {}),
      };
    }
    return { state: "B", chargeID: charge.ID, kwh: charge.EnergyAddedKWh };
  }
  if (drive) {
    return { state: "C", driveID: drive.ID, miles: drive.DistanceMi, miPerKwh: drive.EnergyUsedKWh > 0 ? drive.DistanceMi / drive.EnergyUsedKWh : null };
  }
  return null;
}
