// /charging-cost: the formula, a worked example for the sample R1S, and a calculator that runs in the browser. Every figure on the page
// is an example from the sample truck or comes from what the visitor types; nothing is a measurement of any real owner.

// The worked example, from which the page's figures are computed so they always add up.
export const EXAMPLE = {
  milesPerMonth: 1000,
  milesPerKwh: 2.3,
  coldMilesPerKwh: 1.8,
  homeRate: 0.15,
  offpeakRate: 0.1,
  // One fast-charge stop on the sample two-day Los Angeles to Las Vegas trip: Barstow, 58% to 80% of a 131 kWh pack.
  fastStopKwh: 29,
  homeMonths: 10,
  roadTripStops: 6,
} as const;

// Whole dollars, halves up (a cost of 43.5 is $44).
const usd = (v: number) => Math.round(v + 1e-9);

// The fast-charge price is not in this file or the bundle: the server puts today's typical price seen by DCFC Tracker in the page
// (<script type="application/json" id="charging-cost">) while it is under twelve months old, and leaves it out after.
export type FastPrice = { price: number; month: string };

export function readFastPrice(doc: Document = document, now: Date = new Date()): FastPrice | null {
  try {
    const el = doc.getElementById("charging-cost");
    const j = el?.textContent ? (JSON.parse(el.textContent) as { fast_price?: unknown; price_month?: unknown } | null) : null;
    if (!j || typeof j.fast_price !== "number" || !(j.fast_price > 0) || j.fast_price > 5 || typeof j.price_month !== "string") return null;
    return fastPriceShown(now, j.price_month) ? { price: j.fast_price, month: j.price_month } : null;
  } catch {
    return null;
  }
}

export function workedExample(fast: FastPrice | null = null) {
  const e = EXAMPLE;
  const kwh = Math.round(e.milesPerMonth / e.milesPerKwh);
  const coldKwh = Math.round(e.milesPerMonth / e.coldMilesPerKwh);
  const half = kwh / 2;
  const yearKwh = kwh * e.homeMonths;
  const fastYearKwh = e.fastStopKwh * e.roadTripStops;
  return {
    kwh,
    monthly: usd(kwh * e.homeRate),
    centsPerMile: Math.round(((kwh * e.homeRate) / e.milesPerMonth) * 1000) / 10,
    coldKwh,
    coldMonthly: usd(coldKwh * e.homeRate),
    offpeakMonthly: usd(kwh * e.offpeakRate),
    halfOffpeakMonthly: usd(half * e.offpeakRate + half * e.homeRate),
    fastStop: fast ? usd(e.fastStopKwh * fast.price) : null,
    fastStopAtHome: usd(e.fastStopKwh * e.homeRate),
    yearKwh,
    yearHome: usd(yearKwh * e.homeRate),
    yearOffpeak: usd(yearKwh * e.offpeakRate),
    fastYearKwh,
    fastYear: fast ? usd(fastYearKwh * fast.price) : null,
  };
}

// The tracker's price is only shown for twelve months after the month it was seen.
export function fastPriceShown(now: Date, month: string): boolean {
  const m = /^(\d{4})-(\d{2})$/.exec(month);
  if (!m) return false;
  const until = Date.UTC(Number(m[1]) + 1, Number(m[2]) - 1, 15);
  return now.getTime() <= until;
}

export type CostInput = {
  ratePerKwh: number;
  milesPerMonth: number;
  milesPerKwh: number;
  offpeakRate?: number;
  offpeakSharePct?: number;
  fastKwh?: number;
  fastPrice?: number;
};
export type CostField = keyof CostInput;
export type CostResult =
  | { ok: true; totalKwh: number; homeKwh: number; fastKwh: number; homeCost: number; fastCost: number; total: number; perMile: number; saving: number | null }
  | { ok: false; errors: Partial<Record<CostField, string>> };

const has = (v: number | undefined): v is number => v !== undefined && !Number.isNaN(v);

export function costCalc(i: CostInput): CostResult {
  const errors: Partial<Record<CostField, string>> = {};
  if (!Number.isFinite(i.ratePerKwh) || i.ratePerKwh < 0) errors.ratePerKwh = "Enter your rate per kWh.";
  if (!Number.isFinite(i.milesPerMonth) || i.milesPerMonth <= 0) errors.milesPerMonth = "Enter the distance you drive in a month.";
  if (!Number.isFinite(i.milesPerKwh) || i.milesPerKwh <= 0 || i.milesPerKwh > 10) errors.milesPerKwh = "Enter miles per kWh, between 0 and 10.";
  const offRate = has(i.offpeakRate);
  const offShare = has(i.offpeakSharePct);
  if (offRate && i.offpeakRate! < 0) errors.offpeakRate = "The off-peak rate can't be negative.";
  if (offShare && (i.offpeakSharePct! < 0 || i.offpeakSharePct! > 100)) errors.offpeakSharePct = "Enter a share from 0 to 100.";
  if (offRate && !offShare) errors.offpeakSharePct = "Enter the share of your charging that happens in the off-peak hours.";
  if (offShare && !offRate) errors.offpeakRate = "Enter your off-peak rate.";
  const fastK = has(i.fastKwh);
  const fastP = has(i.fastPrice);
  if (fastK && i.fastKwh! < 0) errors.fastKwh = "Fast-charge energy can't be negative.";
  if (fastP && i.fastPrice! < 0) errors.fastPrice = "The fast-charge price can't be negative.";
  if (fastK && !fastP && i.fastKwh! > 0) errors.fastPrice = "Enter the price you pay per kWh at fast chargers.";
  if (Object.keys(errors).length) return { ok: false, errors };

  const totalKwh = i.milesPerMonth / i.milesPerKwh;
  const fastKwh = Math.min(totalKwh, fastK ? i.fastKwh! : 0);
  const homeKwh = totalKwh - fastKwh;
  const share = offShare ? i.offpeakSharePct! / 100 : 0;
  const offRateV = offRate ? i.offpeakRate! : i.ratePerKwh;
  const homeCost = homeKwh * (share * offRateV + (1 - share) * i.ratePerKwh);
  const fastCost = fastKwh * (fastP ? i.fastPrice! : 0);
  const total = homeCost + fastCost;
  const saving = offRate && share > 0 ? Math.max(0, homeKwh * share * (i.ratePerKwh - offRateV)) : null;
  return { ok: true, totalKwh, homeKwh, fastKwh, homeCost, fastCost, total, perMile: total / i.milesPerMonth, saving };
}
